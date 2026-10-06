package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Runner supervises exactly one instance process: launch, log capture, state
// transitions, command channel and the §5.4 stop ladder.
//
// All exported methods are safe for concurrent use.
type Runner struct {
	opts Options

	mu      sync.RWMutex
	cmd     *exec.Cmd
	pid     int
	ptmx    *os.File // non-nil in PTY mode; also the stdin channel
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	method  termMethod
	degrade string // non-empty => PTY requested, pipes in use (reason)

	pipe     *LogPipe
	sm       *stateMachine
	events   EventSink
	waitOnce sync.Once
	waitCh   chan struct{} // closed when the current process is reaped

	stopMu    sync.Mutex // serialises the stop ladder
	stopping  bool
	startedAt time.Time

	flushCancel context.CancelFunc
	flushDone   chan struct{}
	readDone    chan struct{}

	collector *collector

	// lastStop records the outcome of the most recent stop for observers.
	lastStop StopResult
}

// StopResult describes how an instance was stopped (§5.4). Graceful=false means
// the server was killed and its save may be incomplete — the UI must warn.
type StopResult struct {
	// Method is one of: "none", "command", "ctrl-c", "sigterm", "sigkill".
	Method string `json:"method"`
	// Graceful is true when the process exited before any signal escalation.
	Graceful bool `json:"graceful"`
	// ExitCode is the child's exit status (128+N when signalled).
	ExitCode int `json:"exitCode"`
	// Signal is the terminating signal name, when applicable.
	Signal string `json:"signal,omitempty"`
	// Duration is how long the whole ladder took.
	Duration time.Duration `json:"-"`
	// Waited is how long the graceful stage waited before escalating.
	Waited time.Duration `json:"-"`
	// Err is non-nil when the stop could not be completed cleanly.
	Err error `json:"-"`
}

// NewRunner validates opts and returns a Runner in StateCreated.
func NewRunner(opts Options) (*Runner, error) {
	opts = normalizeOptions(opts)
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	r := &Runner{
		opts:      opts,
		sm:        newStateMachine(),
		events:    nil,
		collector: newCollector(),
	}
	r.waitCh = closedChan() // no process yet
	return r, nil
}

// NewRunnerWithSink is NewRunner plus a global event sink (the Manager's bus).
func NewRunnerWithSink(opts Options, sink EventSink) (*Runner, error) {
	r, err := NewRunner(opts)
	if err != nil {
		return nil, err
	}
	r.events = sink
	return r, nil
}

func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// ID returns the instance id.
func (r *Runner) ID() int64 { return r.opts.ID }

// Options returns a copy of the effective (normalised) options.
func (r *Runner) Options() Options { return r.opts }

// ---------------------------------------------------------------- accessors

// State returns the current lifecycle state.
func (r *Runner) State() State { return r.sm.stateOf() }

// StateVersion returns a monotonically increasing counter bumped on every
// transition. Polling it is cheaper than subscribing.
func (r *Runner) StateVersion() uint64 { return r.sm.versionOf() }

// Snapshot returns the state plus readiness evidence and terminal mode.
func (r *Runner) Snapshot() Snapshot {
	s := r.sm.snapshot()
	r.mu.RLock()
	s.PID = r.pid
	s.Method = string(r.method)
	s.Degraded = r.degrade != ""
	s.DegradedReason = r.degrade
	r.mu.RUnlock()
	return s
}

// PID returns the child pid, or 0 when not running.
func (r *Runner) PID() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pid
}

// TermMode reports the effective I/O mode: "pty" or "pipe".
func (r *Runner) TermMode() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.method == "" {
		return ""
	}
	return string(r.method)
}

// Degraded reports whether a PTY was requested but pipes are in use. When true,
// the console has no ANSI colours and the UI MUST show a warning (§5.2). The
// reason string is also available via Snapshot().DegradedReason.
func (r *Runner) Degraded() (bool, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.degrade != "", r.degrade
}

// EffectiveColorMode returns the colour mode actually in force. Requesting
// "enhanced" on a host without PTYs yields "basic".
func (r *Runner) EffectiveColorMode() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.degrade != "" {
		return ColorModeBasic
	}
	if r.method == methodPTY {
		return ColorModeEnhanced
	}
	return ColorModeBasic
}

// Starts returns the number of successful launches (restart counter).
func (r *Runner) Starts() int { return r.sm.snapshot().Restarts }

// ---------------------------------------------------------------- start

// Start launches the process and returns as soon as it is spawned; readiness is
// asynchronous (State becomes Running once the anchors match). Use WaitReady to
// block for readiness.
func (r *Runner) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.cmd != nil {
		r.mu.Unlock()
		return fmt.Errorf("%w (state %s)", ErrAlreadyRunning, r.State())
	}
	switch r.State() {
	case StateStarting, StateRunning, StateStopping:
		r.mu.Unlock()
		return fmt.Errorf("%w (state %s)", ErrAlreadyRunning, r.State())
	}
	r.mu.Unlock()

	if _, err := os.Stat(r.opts.Dir); err != nil {
		return fmt.Errorf("%w: Dir %s: %w", ErrInvalidOptions, r.opts.Dir, err)
	}

	// Fresh attempt: reset anchors, keep the restart counter.
	r.sm.clearReadiness()

	pipe, err := newLogPipe(r.opts.RingLines, r.opts.LogDir, r.opts.FlushInterval, true)
	if err != nil {
		r.sm.setFailure(err.Error(), nil)
		r.fail(err)
		return err
	}
	pipe.onLine = r.opts.OnLineFn
	pipe.onRecord = r.handleRecord

	cmd := exec.Command(r.opts.Executable, r.opts.Args...)
	cmd.Dir = r.opts.Dir
	cmd.Env = r.buildEnv()
	cmd.SysProcAttr = newSysProcAttr()

	var (
		stdin      io.WriteCloser
		stdout     io.ReadCloser
		ptmx       *os.File
		method     termMethod
		degradeMsg string
	)

	if r.opts.wantsPTY() {
		f, perr := ptyOpen(cmd, r.opts.Rows, r.opts.Cols)
		if perr != nil {
			// Fall back to pipes, but never silently (§5.2). The child has not
			// been started by the failed pty.StartWithSize (it returns before
			// exec when the PTY cannot be allocated), so reusing cmd is safe —
			// but we rebuild it defensively to avoid any half-set state.
			cmd = exec.Command(r.opts.Executable, r.opts.Args...)
			cmd.Dir = r.opts.Dir
			cmd.Env = r.buildEnv()
			cmd.SysProcAttr = newSysProcAttr()
			in, out, werr := wirePipes(cmd)
			if werr != nil {
				_ = pipe.close()
				r.sm.setFailure(werr.Error(), nil)
				r.fail(werr)
				return &startError{Stage: "wire pipes", Err: werr}
			}
			stdin, stdout, method = in, out, methodPipe
			degradeMsg = fmt.Sprintf("PTY unavailable, falling back to pipes (no ANSI colours): %v", perr)
		} else {
			// In PTY mode the master is a single read/write endpoint: stdin,
			// stdout and stderr are all the same terminal (§4.3).
			ptmx = f
			stdout = f
			stdin = nil
			method = methodPTY
		}
	} else {
		in, out, werr := wirePipes(cmd)
		if werr != nil {
			_ = pipe.close()
			r.sm.setFailure(werr.Error(), nil)
			r.fail(werr)
			return &startError{Stage: "wire pipes", Err: werr}
		}
		stdin, stdout, method = in, out, methodPipe
	}

	if err := cmd.Start(); err != nil {
		_ = pipe.close()
		if ptmx != nil {
			_ = ptmx.Close()
		}
		r.sm.setFailure(err.Error(), nil)
		r.fail(&startError{Stage: "exec " + r.opts.Executable, Err: err})
		return &startError{Stage: "exec " + r.opts.Executable, Err: err}
	}

	r.mu.Lock()
	r.cmd = cmd
	r.pid = cmd.Process.Pid
	r.stdin = stdin
	r.stdout = stdout
	r.ptmx = ptmx
	r.method = method
	r.degrade = degradeMsg
	r.pipe = pipe
	r.startedAt = time.Now()
	r.waitCh = make(chan struct{})
	waitCh := r.waitCh
	r.mu.Unlock()

	// A new pid inherits no CPU baseline: without this reset the first sample
	// of a restarted instance would report a bogus spike.
	r.collector.Reset()

	r.sm.setAlive(true)
	r.sm.incRestarts()

	if up, ok := r.sm.transition(StateStarting, StateCreated, StateStopped, StateCrashed, StateFailed); ok {
		r.publish(r.transitionEvent(up))
	}

	// Flusher goroutine: bounded and cancelled by stop().
	fctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.mu.Lock()
	r.flushCancel = cancel
	r.flushDone = done
	r.mu.Unlock()
	go func() {
		defer close(done)
		pipe.runFlusher(fctx, r.opts.FlushInterval)
	}()

	r.publish(Event{
		Type:    EventProcessStarted,
		ID:      r.opts.ID,
		PID:     cmd.Process.Pid,
		Message: fmt.Sprintf("started %s %s (pid %d, %s)", r.opts.Executable, strings.Join(r.opts.Args, " "), cmd.Process.Pid, method),
		Method:  string(method),
		Reason:  degradeMsg,
	})

	if degradeMsg != "" {
		r.publish(Event{
			Type:    EventDegraded,
			ID:      r.opts.ID,
			PID:     cmd.Process.Pid,
			Method:  string(methodPipe),
			Reason:  degradeMsg,
			Message: degradeMsg,
		})
	}

	// Reader + waiter goroutines. readDone lets waitLoop wait for the decoder
	// tail to be flushed before it closes the pipe, so Stop never loses lines.
	readDone := make(chan struct{})
	r.mu.Lock()
	r.readDone = readDone
	r.mu.Unlock()
	go r.readLoop(stdout, pipe, readDone)
	go r.waitLoop(cmd, waitCh, pipe, method, ptmx, readDone)

	// Start timeout: Starting -> Failed.
	go r.startTimer(r.opts.StartTimeout)

	return nil
}

// buildEnv assembles the child environment (§4.3). Extra vars are appended, so
// they win over inherited ones.
func (r *Runner) buildEnv() []string {
	env := os.Environ()
	add := func(k, v string) {
		if v == "" {
			return
		}
		env = append(env, k+"="+v)
	}
	add("TERM", r.opts.Term)
	add("LANG", "C.UTF-8")
	add("LC_ALL", "C.UTF-8")
	add("DOTNET_CLI_TELEMETRY_OPTOUT", "1")
	add("DOTNET_ROOT", r.opts.DOTNETRoot)
	add("TZ", r.opts.TZ)
	env = append(env, r.opts.Env...)
	return env
}

// startTimer fails a start that never reaches readiness.
func (r *Runner) startTimer(d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		r.sm.mu.Lock()
		running := r.sm.state == StateStarting
		r.sm.mu.Unlock()
		if !running {
			return
		}
		// Anchor-matched readiness never happened: force the process down and
		// report Failed with the missing evidence.
		snap := r.Snapshot()
		msg := fmt.Sprintf("startup timeout after %s (alive=%v listening=%v screen=%v)",
			d, snap.Alive, snap.Listening, snap.WorldDone)
		r.sm.setFailure(msg, nil)
		if up, ok := r.sm.transition(StateFailed, StateStarting); ok {
			r.publish(r.transitionEvent(up))
		}
		r.forceStopAfterFailure()
	case <-r.currentWaitCh():
	}
}

// currentWaitCh reads the wait channel under the lock. The startup-timer
// goroutine can outlive the process it was started for (a restart replaces
// waitCh), so this read must be synchronised.
func (r *Runner) currentWaitCh() <-chan struct{} {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.waitCh
}

// forceStopAfterFailure tears down a process that failed to become ready.
func (r *Runner) forceStopAfterFailure() {
	r.mu.RLock()
	pid := r.pid
	r.mu.RUnlock()
	if pid <= 0 {
		return
	}
	// SIGTERM, then SIGKILL shortly after: a hung startup must not linger.
	_ = signalGroup(pid, syscallSIGTERM)
	go func() {
		select {
		case <-r.waitCh:
		case <-time.After(r.opts.KillTimeout):
			_ = signalGroup(pid, syscallSIGKILL)
		}
	}()
}

// ---------------------------------------------------------------- read/wait

// readLoop pumps raw bytes into the log pipe until EOF. It is the SOLE owner of
// the decoder for the duration of one process, which is why the flush lives here
// and nowhere else: ansi.Decoder is explicitly not safe for concurrent use.
func (r *Runner) readLoop(rd io.Reader, pipe *LogPipe, done chan<- struct{}) {
	if done != nil {
		defer close(done)
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := rd.Read(buf)
		if n > 0 {
			pipe.Ingest(buf[:n])
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
				// A read error on a PTY master usually means the slave side is
				// gone; log it for diagnostics and let the waiter decide.
				pipe.Ingest([]byte("[scnetm] read error: " + err.Error() + "\n"))
			}
			// Drain the decoder's tail and push it to the sink. This is the
			// last thing the reader does, so the tail can never be lost.
			pipe.Flush()
			return
		}
	}
}

// waitLoop reaps the child, flushes the tail of the log, closes the terminal
// and drives the exit transitions.
func (r *Runner) waitLoop(cmd *exec.Cmd, waitCh chan struct{}, pipe *LogPipe, method termMethod, ptmx *os.File, readDone <-chan struct{}) {
	err := cmd.Wait()
	code, sig := waitExitCode(err)

	// The reader may still be draining; give it a bounded moment, then force
	// the pipe closed so no lines are lost and no goroutine hangs.
	if ptmx != nil {
		_ = ptmx.Close()
	} else {
		r.mu.RLock()
		out := r.stdout
		r.mu.RUnlock()
		if c, ok := out.(io.Closer); ok && out != nil {
			_ = c.Close()
		}
	}
	// Close the terminal above so readLoop sees EOF, then WAIT for it: the reader
	// owns the decoder and performs the final flush, so waiting here is what
	// guarantees no tail line is lost. It is bounded because the descriptor is
	// already closed.
	if readDone != nil {
		select {
		case <-readDone:
		case <-time.After(5 * time.Second):
		}
	}
	// Only the disk buffers remain for this goroutine to flush.
	pipe.FlushDisk()

	r.mu.Lock()
	r.cmd = nil
	r.pid = 0
	r.stdin = nil
	r.stdout = nil
	r.ptmx = nil
	r.method = ""
	pipeRef := r.pipe
	r.pipe = nil
	cancel := r.flushCancel
	done := r.flushDone
	r.flushCancel, r.flushDone = nil, nil
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	if pipeRef != nil {
		_ = pipeRef.close()
	}

	close(waitCh)

	r.sm.setAlive(false)
	r.sm.setExitCode(&code)

	r.emitExit(code, sig)

	// Transition selection is the whole point of §5.1: an exit while Running is
	// a crash, an exit while Stopping is a clean stop, an exit while Starting
	// (before readiness) is a failure.
	cur := r.State()
	switch cur {
	case StateRunning:
		r.sm.setFailure(fmt.Sprintf("process exited unexpectedly (code %d%s)", code, sigSuffix(sig)), &code)
		if up, ok := r.sm.transition(StateCrashed, StateRunning); ok {
			r.publish(r.transitionEvent(up))
		}
	case StateStopping:
		if up, ok := r.sm.transition(StateStopped, StateStopping); ok {
			r.publish(r.transitionEvent(up))
		}
	case StateStarting:
		r.sm.setFailure(fmt.Sprintf("process exited before readiness (code %d%s)", code, sigSuffix(sig)), &code)
		if up, ok := r.sm.transition(StateFailed, StateStarting); ok {
			r.publish(r.transitionEvent(up))
		}
	default:
		if code != 0 {
			r.sm.setFailure(fmt.Sprintf("process exited (code %d%s)", code, sigSuffix(sig)), &code)
		}
	}
}

func sigSuffix(sig string) string {
	if sig == "" {
		return ""
	}
	return ", signal " + sig
}

func (r *Runner) emitExit(code int, sig string) {
	c := code
	r.publish(Event{
		Type:     EventProcessExited,
		ID:       r.opts.ID,
		ExitCode: &c,
		Signal:   sig,
		Message:  fmt.Sprintf("process exited (code %d%s)", code, sigSuffix(sig)),
	})
}

// handleRecord runs the anchor rules on the ANSI-stripped text and drives the
// state machine. This is where "readiness requires all three" lives (§5.1).
func (r *Runner) handleRecord(rec LogRecord) {
	if rec.Plain == "" {
		return
	}
	matches := applyRules(rec.Plain)
	if len(matches) == 0 {
		return
	}
	becameReady := false
	for _, m := range matches {
		switch m.Kind {
		case kindTermMode:
			r.publish(Event{
				Type:    EventTermMode,
				ID:      r.opts.ID,
				Text:    m.Mode,
				Rule:    m.Rule,
				Line:    rec.Plain,
				Raw:     rec.Raw,
				Message: "terminal mode: " + m.Mode,
			})
		case kindListening:
			if p := m.portValue(); p > 0 {
				r.sm.setPort(p)
			}
			r.publish(Event{
				Type: EventState, ID: r.opts.ID, Rule: m.Rule,
				Port: portOr(m, r.snapshotPort()), Line: rec.Plain, Raw: rec.Raw,
				Message: "server listening",
			})
		case kindWorldLoaded:
			r.sm.setWorld(m.Value)
			r.publish(Event{
				Type: EventState, ID: r.opts.ID, Rule: m.Rule,
				WorldName: m.Value, Line: rec.Plain, Raw: rec.Raw,
				Message: "world loaded: " + m.Value,
			})
		case kindReady:
			r.publish(Event{
				Type: EventState, ID: r.opts.ID, Rule: m.Rule,
				Line: rec.Plain, Raw: rec.Raw, Message: "reached game screen",
			})
		case kindPlugin:
			r.publish(Event{
				Type: EventPluginLoaded, ID: r.opts.ID, Rule: m.Rule,
				Plugin: m.Value, Line: rec.Plain, Raw: rec.Raw,
				Message: "plugin loaded: " + m.Value,
			})
		case kindError:
			r.publish(Event{
				Type: EventLogError, ID: r.opts.ID, Rule: m.Rule,
				Line: rec.Plain, Raw: rec.Raw, Message: rec.Plain,
			})
		case kindCrash:
			r.publish(Event{
				Type: EventLogCrash, ID: r.opts.ID, Rule: m.Rule,
				Line: rec.Plain, Raw: rec.Raw, Message: rec.Plain,
			})
		}
		if m.Kind == kindListening || m.Kind == kindWorldLoaded || m.Kind == kindReady {
			becameReady = true
		}
	}
	if becameReady {
		r.maybeReady()
	}
}

func (r *Runner) snapshotPort() int { return r.sm.snapshot().Port }

func portOr(m ruleMatch, fallback int) int {
	if p := m.portValue(); p > 0 {
		return p
	}
	return fallback
}

// maybeReady promotes Starting -> Running only when all three readiness
// conditions hold: alive AND listening anchor AND Game screen anchor.
func (r *Runner) maybeReady() {
	if !r.sm.readyNow() {
		return
	}
	if up, ok := r.sm.transition(StateRunning, StateStarting); ok {
		r.publish(r.transitionEvent(up))
	}
}

// ---------------------------------------------------------------- command

// SendCommand writes one line to the instance's stdin (§5.3).
//
// Validation is strict on purpose: the line is written verbatim into a terminal,
// so an embedded CR/LF would inject a second command.
func (r *Runner) SendCommand(line string) error {
	if err := ValidateCommand(line); err != nil {
		return err
	}
	r.mu.RLock()
	f := r.ptmx
	in := r.stdin
	r.mu.RUnlock()
	if f == nil && in == nil {
		return ErrNoCommandChannel
	}
	payload := line + "\n"
	if f != nil {
		_, err := f.Write([]byte(payload))
		return err
	}
	_, err := in.Write([]byte(payload))
	return err
}

// ValidateCommand applies the command-channel rules without sending anything.
// The API layer uses it for a 400 response before touching the process.
func ValidateCommand(line string) error {
	if strings.TrimSpace(line) == "" {
		return ErrEmptyCommand
	}
	if len(line) > MaxCommandBytes {
		return fmt.Errorf("%w: %d bytes > %d", ErrCommandTooLong, len(line), MaxCommandBytes)
	}
	if strings.ContainsAny(line, "\r\n") {
		return ErrInvalidCommand
	}
	return nil
}

// SetSize resizes the PTY. Returns ErrNoCommandChannel in pipe mode.
func (r *Runner) SetSize(rows, cols uint16) error {
	r.mu.RLock()
	f := r.ptmx
	r.mu.RUnlock()
	if f == nil {
		return ErrNoCommandChannel
	}
	return setPTYSize(f, rows, cols)
}

// ---------------------------------------------------------------- logging

// Subscribe attaches a live log subscriber. replayLines replays that many
// recent lines first; raw selects whether Raw (ANSI) fields are populated.
func (r *Runner) Subscribe(ctx context.Context, replayLines int, raw bool) (*Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	pipe := r.pipe
	r.mu.RUnlock()
	if pipe == nil {
		return nil, fmt.Errorf("%w: instance not running", ErrNoCommandChannel)
	}
	s, err := pipe.subscribe(replayLines, raw)
	if err != nil {
		return nil, err
	}
	// Tie the subscription lifetime to ctx so a disconnecting WebSocket cannot
	// leak a subscriber.
	if ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				s.Close()
			case <-s.closed:
			}
		}()
	}
	return s, nil
}

// Replay returns up to n recent records, oldest first. It works while the
// instance is stopped only if the Runner still holds a pipe (it does not after
// a stop), so callers should persist logs for offline viewing.
func (r *Runner) Replay(ctx context.Context, n int, raw bool) []LogRecord {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil
		}
	}
	r.mu.RLock()
	pipe := r.pipe
	r.mu.RUnlock()
	if pipe == nil {
		return nil
	}
	return pipe.Replay(n, raw)
}

// ---------------------------------------------------------------- stop

// Stop runs the §5.4 ladder. It is idempotent and safe to call concurrently:
// concurrent callers block on the same sequence and observe the same result.
func (r *Runner) Stop(ctx context.Context) (StopResult, error) {
	r.stopMu.Lock()
	defer r.stopMu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()

	r.mu.RLock()
	pid := r.pid
	r.mu.RUnlock()

	if pid <= 0 {
		switch st := r.State(); st {
		case StateCreated, StateStopped, StateCrashed, StateFailed:
			res := StopResult{Method: "none", Graceful: true, Duration: time.Since(start)}
			if snap := r.sm.snapshot(); snap.ExitCode != nil {
				res.ExitCode = *snap.ExitCode
			}
			r.mu.Lock()
			r.lastStop = res
			r.mu.Unlock()
			return res, fmt.Errorf("%w (state %s)", ErrAlreadyStopped, st)
		}
		return StopResult{Method: "none", Duration: time.Since(start)}, ErrNotRunning
	}

	if up, ok := r.sm.transition(StateStopping, StateRunning, StateStarting); ok {
		r.publish(r.transitionEvent(up))
	}

	waitCh := r.waitCh
	res := StopResult{Method: "none"}

	// Stage 1 — graceful: /stop through the command channel. The plan notes
	// CmdStop.ProcessCmd() is an empty stub, so this is best-effort only and a
	// failure here must not abort the ladder.
	sentCommand := false
	if err := r.SendCommand("/stop"); err == nil {
		sentCommand = true
	}
	// Stage 1b (PTY only) — Ctrl+C, which is what actually reaches the
	// server's 增强终端安全退出 path (§5.4).
	sentCtrlC := false
	if r.opts.wantsPTY() {
		r.mu.RLock()
		f := r.ptmx
		r.mu.RUnlock()
		if f != nil {
			if _, err := f.Write([]byte{0x03}); err == nil {
				sentCtrlC = true
			}
		}
	}

	gracefulMethod := "none"
	switch {
	case sentCtrlC:
		gracefulMethod = "ctrl-c"
	case sentCommand:
		gracefulMethod = "command"
	}

	if r.awaitExit(ctx, waitCh, r.opts.StopTimeout) {
		res.Method = gracefulMethod
		res.Graceful = true
		res.Waited = time.Since(start)
		res.Duration = time.Since(start)
		res.ExitCode, res.Signal = r.exitStatus()
		r.finishStop(res)
		return res, nil
	}

	// Stage 2 — SIGTERM to the whole process group.
	if err := signalGroup(pid, syscallSIGTERM); err != nil {
		res.Err = fmt.Errorf("SIGTERM to group %d: %w", pid, err)
	}
	if r.awaitExit(ctx, waitCh, r.opts.KillTimeout) {
		res.Method = "sigterm"
		res.Graceful = false
		res.Waited = time.Since(start)
		res.Duration = time.Since(start)
		res.ExitCode, res.Signal = r.exitStatus()
		r.finishStop(res)
		return res, nil
	}

	// Stage 3 — SIGKILL. The save may be incomplete: Graceful stays false and
	// the caller MUST warn the user (§5.4).
	if err := signalGroup(pid, syscallSIGKILL); err != nil && res.Err == nil {
		res.Err = fmt.Errorf("SIGKILL to group %d: %w", pid, err)
	}
	// SIGKILL cannot be ignored, so this wait is bounded but effectively final.
	if !r.awaitExit(ctx, waitCh, r.opts.KillTimeout) {
		if res.Err == nil {
			res.Err = fmt.Errorf("process %d did not exit after SIGKILL", pid)
		}
	}
	res.Method = "sigkill"
	res.Graceful = false
	res.Waited = time.Since(start)
	res.Duration = time.Since(start)
	res.ExitCode, res.Signal = r.exitStatus()
	r.finishStop(res)
	return res, res.Err
}

// awaitExit waits for the process to be reaped, the timeout, or ctx.
func (r *Runner) awaitExit(ctx context.Context, waitCh <-chan struct{}, d time.Duration) bool {
	if d <= 0 {
		d = DefaultStopTimeout
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-waitCh:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		// Caller gave up: report not-exited so the caller keeps escalating.
		select {
		case <-waitCh:
			return true
		default:
			return false
		}
	}
}

// exitStatus reads the exit code recorded by waitLoop.
func (r *Runner) exitStatus() (int, string) {
	snap := r.sm.snapshot()
	if snap.ExitCode != nil {
		return *snap.ExitCode, signalName(*snap.ExitCode)
	}
	return -1, ""
}

// signalName maps a 128+N exit code back to a signal name.
func signalName(code int) string {
	if code > 128 && code < 128+32 {
		if s := unixSignalName(code - 128); s != "" {
			return s
		}
	}
	return ""
}

func (r *Runner) finishStop(res StopResult) {
	r.mu.Lock()
	r.lastStop = res
	r.mu.Unlock()

	// The waiter goroutine owns the final Stopping -> Stopped transition, so
	// wait for it here. Stop must not return while the instance still reports
	// "stopping", or callers would have to poll.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.WaitState(ctx, func(s State) bool { return s.Terminal() })
	// Guarantee the tail of the log reached disk before the caller is told the
	// instance is stopped (§5.2 / definition of done).
	r.sm.mu.Lock()
	st := r.sm.state
	r.sm.mu.Unlock()
	if st == StateStopped {
		r.publish(Event{
			Type:     EventProcessExited,
			ID:       r.opts.ID,
			Message:  fmt.Sprintf("stopped via %s (graceful=%v, code=%d)", res.Method, res.Graceful, res.ExitCode),
			ExitCode: &res.ExitCode,
			Method:   res.Method,
			Signal:   res.Signal,
		})
	}
}

// LastStop returns the outcome of the most recent stop.
func (r *Runner) LastStop() StopResult {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastStop
}

// Restart stops (if needed) and starts again.
//
// It waits for the stop to fully settle (state reaches a terminal value) before
// starting. Without that wait, the caller can race the waiter goroutine's final
// Stopping -> Stopped transition and get ErrAlreadyRunning.
func (r *Runner) Restart(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if r.State().Active() {
		if _, err := r.Stop(ctx); err != nil && !errors.Is(err, ErrAlreadyStopped) {
			return fmt.Errorf("restart: stop phase: %w", err)
		}
	}
	if err := r.WaitState(ctx, func(s State) bool { return s.Terminal() }); err != nil {
		return fmt.Errorf("restart: waiting for the previous process to settle: %w", err)
	}
	return r.Start(ctx)
}

// ---------------------------------------------------------------- waiting

// Wait blocks until the current process is reaped and the exit transition has
// been applied. It returns immediately when no process is running.
func (r *Runner) Wait() {
	r.mu.RLock()
	ch := r.waitCh
	r.mu.RUnlock()
	<-ch
}

// WaitContext is Wait with cancellation.
func (r *Runner) WaitContext(ctx context.Context) error {
	r.mu.RLock()
	ch := r.waitCh
	r.mu.RUnlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitState blocks until the state satisfies pred, or ctx is done. It uses the
// transition broadcast, so it costs nothing while idle.
func (r *Runner) WaitState(ctx context.Context, pred func(State) bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if pred(r.State()) {
			return nil
		}
		ch := r.sm.changeChan()
		select {
		case <-ch:
		case <-ctx.Done():
			if pred(r.State()) {
				return nil
			}
			return fmt.Errorf("%w (state %s)", ctx.Err(), r.State())
		}
	}
}

// WaitReady blocks until readiness (Running + all anchors) or ctx expiry.
func (r *Runner) WaitReady(ctx context.Context) error {
	return r.WaitState(ctx, func(s State) bool {
		if s == StateFailed || s == StateCrashed || s == StateStopped {
			return true
		}
		return s == StateRunning
	})
}

// Ready reports the full readiness predicate.
func (r *Runner) Ready() bool { return r.Snapshot().Ready() }

// ---------------------------------------------------------------- events

// publish sends an event to the local callback and the global sink.
func (r *Runner) publish(ev Event) {
	r.emit(ev)
	if r.events != nil {
		r.events(ev)
	}
}

// fail transitions to Failed from a pre-start error.
func (r *Runner) fail(err error) {
	if up, ok := r.sm.transition(StateFailed, StateCreated, StateStopped, StateCrashed, StateFailed); ok {
		r.publish(r.transitionEvent(up))
	}
	_ = err
}
