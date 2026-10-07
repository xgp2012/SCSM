package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
)

// This file defines the supervisor facade the API layer consumes.
//
// It deliberately does NOT import internal/supervisor: that package is owned by
// another agent and is written in parallel. Declaring the interface here means
// the API compiles, tests and runs standalone, and wiring the real supervisor
// later is a small adapter (see adapters.go) rather than a handler rewrite.
//
// NopProcessManager is not merely a stub that errors: it is a working in-memory
// lifecycle so that create → start → stats → stop is exercisable on a machine
// with no .NET runtime and no game server package (see the environment note in
// the task). The only operations it refuses are the ones that genuinely need a
// process: SendCommand and metrics beyond the synthesised uptime.

// Instance run states. These mirror the state machine in §5.1 and are the
// vocabulary of instances.state, instance_state.state and the /ws/events
// payloads. They are plain strings so the API never has to translate between
// the supervisor's enum and JSON.
const (
	// StateCreated is a freshly created instance that has never been started.
	StateCreated = "Created"
	// StateStarting covers process spawn through to readiness.
	StateStarting = "Starting"
	// StateRunning is a ready, accepting-connections server.
	StateRunning = "Running"
	// StateStopping is a graceful stop in progress.
	StateStopping = "Stopping"
	// StateStopped is a cleanly exited instance.
	StateStopped = "Stopped"
	// StateCrashed is an unexpected exit.
	StateCrashed = "Crashed"
	// StateRestarting is a restart in progress.
	StateRestarting = "Restarting"
	// StateUnknown is used when no state is known at all.
	StateUnknown = "Unknown"
)

// StopResult reports the outcome of a stop request.
type StopResult struct {
	// InstanceID the stop applied to.
	InstanceID int64 `json:"instance_id"`
	// Forced reports whether the graceful path was abandoned for a kill.
	Forced bool `json:"forced"`
	// Exited reports whether the process actually terminated. A false value
	// with no error means the supervisor gave up waiting; the API turns that
	// into 504.
	Exited bool `json:"exited"`
	// ExitCode is the process exit status when known.
	ExitCode *int `json:"exit_code,omitempty"`
	// DurationMS is how long the stop took.
	DurationMS int64 `json:"duration_ms"`
}

// StateInfo is the supervisor's view of one instance.
type StateInfo struct {
	// State is one of the State* constants.
	State string `json:"state"`
	// PID is the process id when running, 0 otherwise.
	PID int `json:"pid,omitempty"`
	// StartedAt is when the current run began.
	StartedAt *time.Time `json:"started_at,omitempty"`
	// StoppedAt is when the last run ended.
	StoppedAt *time.Time `json:"stopped_at,omitempty"`
	// ExitCode of the last run, when it has exited at least once.
	ExitCode *int `json:"exit_code,omitempty"`
	// Restarts counts automatic restarts in the current supervision window.
	Restarts int `json:"restarts"`
	// Ready reports whether the server signalled readiness (the log-parsed
	// criterion from §5.2.1). A Running-but-not-Ready instance is still
	// booting.
	Ready bool `json:"ready"`
	// LastError carries the most recent failure reason.
	LastError string `json:"last_error,omitempty"`
	// OnlinePlayers is the last parsed player count, or -1 when the command
	// channel that would provide it is unavailable (§6.6: "需指令通道").
	OnlinePlayers int `json:"online_players"`
}

// MetricsInfo holds the per-process metrics from §6.6.
type MetricsInfo struct {
	InstanceID   int64   `json:"instance_id"`
	PID          int     `json:"pid,omitempty"`
	CPUPercent   float64 `json:"cpu_percent"`
	MemoryBytes  int64   `json:"memory_bytes"`
	MemoryRSS    int64   `json:"memory_rss_bytes"`
	Threads      int     `json:"threads"`
	OpenFDs      int     `json:"open_fds"`
	ReadBytes    int64   `json:"read_bytes"`
	WrittenBytes int64   `json:"written_bytes"`
	// UptimeSeconds is wall-clock since the current run started.
	UptimeSeconds int64 `json:"uptime_seconds"`
	// Restarts and LastExitCode mirror StateInfo for the stats endpoint.
	Restarts     int  `json:"restarts"`
	LastExitCode *int `json:"last_exit_code,omitempty"`
	// Available reports whether real metrics could be collected. When false,
	// the numeric fields are zero and the UI should render "unavailable"
	// rather than "0%".
	Available bool `json:"available"`
	// Note explains an unavailable result.
	Note string `json:"note,omitempty"`
	// CollectedAt is the sample time.
	CollectedAt time.Time `json:"collected_at"`
}

// LogLine is one framed line from an instance's console.
type LogLine struct {
	InstanceID int64     `json:"instance_id"`
	Seq        uint64    `json:"seq"`
	Timestamp  time.Time `json:"ts"`
	// Stream is "stdout" or "stderr".
	Stream string `json:"stream"`
	// Text is the line with ANSI sequences intact when Raw was requested,
	// stripped otherwise.
	Text string `json:"text"`
	// Raw mirrors the subscription's raw flag so clients can tell which
	// representation they received.
	Raw bool `json:"raw"`
	// Level is the parsed severity (info/warn/error) when the log parser
	// recognised one; empty for unparsed lines.
	Level string `json:"level,omitempty"`
	// Event is the parsed §5.2.1 event name (e.g. "world_loaded") when
	// recognised.
	Event string `json:"event,omitempty"`
}

// LogSubscription is a live console feed for one instance.
//
// Close must be idempotent and must not close the returned channels twice; the
// WebSocket handler relies on that to make its cleanup path re-entrant.
type LogSubscription interface {
	// Lines is closed by the supervisor when the feed ends (instance stopped,
	// server shutting down).
	Lines() <-chan LogLine
	// History returns up to n buffered lines (newest last) for replay on
	// connect.
	//
	// It may return nil, which the WebSocket handler treats as "no separate
	// replay buffer" and tolerates. That is the correct implementation for a
	// supervisor that seeds Lines() with its own backlog on subscribe (as
	// internal/supervisor does): draining Lines() to build a second buffer here
	// would race the reader and swallow live lines. Returning nil costs only
	// backfill, never correctness — a client that reconnects to such a
	// supervisor sees live lines from that point on.
	History(n int) []LogLine
	// Close stops the feed and releases its resources. Subsequent calls are
	// no-ops.
	Close() error
}

// ProcessManager is the supervisor facade.
//
// Contract notes for the supervisor implementer:
//
//   - Start on an already-Running instance must return an error wrapping
//     ErrConflict so the handler answers 409 (the plan pins this status).
//   - Stop must honour ctx: if the graceful timeout expires it returns a
//     StopResult with Exited=false (no error), which the handler maps to 504.
//     Returning an error is also acceptable and maps to 503/504 by class.
//   - State must never block and must be safe to call for an unknown
//     instance (ok=false).
//   - SendCommand must reject input containing CR/LF and input longer than
//     CommandMaxBytes, in addition to the API-layer check.
//   - Subscribe returns a LogSubscription; replay requests that many
//     historical lines before live ones.
type ProcessManager interface {
	Start(ctx context.Context, instanceID int64) error
	Stop(ctx context.Context, instanceID int64, force bool) (*StopResult, error)
	Restart(ctx context.Context, instanceID int64) error
	State(instanceID int64) (StateInfo, bool)
	Metrics(instanceID int64) (MetricsInfo, error)
	SendCommand(ctx context.Context, instanceID int64, line string) error
	Subscribe(ctx context.Context, instanceID int64, replay int, raw bool) (LogSubscription, error)
}

// CommandMaxBytes caps a console command line, counted in bytes.
//
// Defense in depth (§5.7): the supervisor performs its own validation, but the
// API layer refuses oversize or multi-line input before it ever reaches the
// process stdin, so a bug or a future direct-call path cannot inject a second
// command.
const CommandMaxBytes = 1024

// ErrCommandRejected is returned by ValidateCommand for any rejected input.
var ErrCommandRejected = errors.New("指令被拒绝")

// ValidateCommand applies the API layer's command-injection defenses.
//
// Rationale: the game server reads commands from stdin, one per line. An
// attacker who can embed \n or \r can therefore smuggle extra commands. The
// supervisor writes to a PTY rather than a shell (so classic shell metacharacter
// injection does not apply), but rejecting CR/LF is still mandatory, and
// control characters in general are rejected because they can drive terminal
// escape sequences into the log stream.
func ValidateCommand(line string) (string, error) {
	trimmed := strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(trimmed) == "" {
		return "", fmt.Errorf("%w: command is empty", ErrCommandRejected)
	}
	if len(trimmed) > CommandMaxBytes {
		return "", fmt.Errorf("%w: command exceeds %d bytes", ErrCommandRejected, CommandMaxBytes)
	}
	for i, r := range trimmed {
		switch {
		case r == '\n' || r == '\r':
			return "", fmt.Errorf("%w: command must not contain newline or carriage return (offset %d)", ErrCommandRejected, i)
		case r == 0:
			return "", fmt.Errorf("%w: command must not contain a NUL byte", ErrCommandRejected)
		case r == 0x1b:
			return "", fmt.Errorf("%w: command must not contain an escape character", ErrCommandRejected)
		case unicode.IsControl(r) && r != '\t':
			return "", fmt.Errorf("%w: command must not contain control character U+%04X", ErrCommandRejected, r)
		}
	}
	return trimmed, nil
}

// ---------------------------------------------------------------------------
// NopProcessManager
// ---------------------------------------------------------------------------

// NopProcessManager is a working in-memory lifecycle with no real process
// behind it.
//
// Purpose: the panel must start and serve even when no game server and no
// .NET runtime exist (the environment this is developed in). With this manager:
//
//   - Start/Stop/Restart transition a plausible state machine and emit events.
//   - State and Metrics return coherent, obviously-synthetic values.
//   - Subscribe yields a feed that emits a startup banner and then stays open,
//     so the console WebSocket round-trips end to end in tests.
//   - SendCommand is accepted and echoed back on the instance's log feed
//     (prefixed) so the console's command path is fully exercisable.
//
// What it does NOT do: run anything. Metrics are marked Available=false so the
// UI shows "unavailable" rather than a fake 0% CPU, and the reason is spelled
// out in Note.
type NopProcessManager struct {
	log Logger

	mu       sync.Mutex
	states   map[int64]*StateInfo
	subs     map[int64]map[*nopSubscription]struct{}
	seq      map[int64]uint64
	history  map[int64][]LogLine
	started  map[int64]time.Time
	now      func() time.Time
	restarts map[int64]int
}

// NewNopProcessManager builds a NopProcessManager.
func NewNopProcessManager(log Logger) *NopProcessManager {
	if log == nil {
		log = NewDiscardLogger()
	}
	return &NopProcessManager{
		log:      log,
		states:   make(map[int64]*StateInfo),
		subs:     make(map[int64]map[*nopSubscription]struct{}),
		seq:      make(map[int64]uint64),
		history:  make(map[int64][]LogLine),
		started:  make(map[int64]time.Time),
		restarts: make(map[int64]int),
		now:      time.Now,
	}
}

// Compile-time assertion that the nop satisfies the interface.
var _ ProcessManager = (*NopProcessManager)(nil)

// Start transitions the instance to Running.
func (n *NopProcessManager) Start(_ context.Context, instanceID int64) error {
	n.mu.Lock()
	st, ok := n.states[instanceID]
	if !ok {
		st = &StateInfo{State: StateCreated, OnlinePlayers: -1}
		n.states[instanceID] = st
	}
	switch st.State {
	case StateRunning, StateStarting:
		n.mu.Unlock()
		return fmt.Errorf("%w: instance %d is already %s", ErrConflict, instanceID, st.State)
	case StateStopping:
		n.mu.Unlock()
		return fmt.Errorf("%w: instance %d is stopping", ErrConflict, instanceID)
	}

	now := n.now()
	st.State = StateRunning
	st.Ready = true
	st.PID = 0 // no real process in the nop manager
	st.StartedAt = &now
	st.LastError = ""
	n.started[instanceID] = now
	n.mu.Unlock()

	n.emit(instanceID, "stdout", "[scnetm] no-op process manager: instance marked Running (no game server is installed in this build)")
	return nil
}

// Stop transitions the instance to Stopped.
//
// It respects ctx cancellation and honours the force flag, so the 504 path can
// be exercised by a caller with an already-expired context.
func (n *NopProcessManager) Stop(ctx context.Context, instanceID int64, force bool) (*StopResult, error) {
	start := n.now()

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	n.mu.Lock()
	st, ok := n.states[instanceID]
	if !ok {
		st = &StateInfo{State: StateStopped, OnlinePlayers: -1}
		n.states[instanceID] = st
	}
	if st.State == StateStopped || st.State == StateCreated {
		n.mu.Unlock()
		return &StopResult{
			InstanceID: instanceID,
			Forced:     force,
			Exited:     true,
			DurationMS: n.now().Sub(start).Milliseconds(),
		}, nil
	}
	now := n.now()
	st.State = StateStopped
	st.Ready = false
	st.StoppedAt = &now
	st.OnlinePlayers = 0
	pid := st.PID
	st.PID = 0
	n.mu.Unlock()

	n.emit(instanceID, "stdout", "[scnetm] no-op process manager: instance stopped")
	_ = pid
	return &StopResult{
		InstanceID: instanceID,
		Forced:     force,
		Exited:     true,
		DurationMS: n.now().Sub(start).Milliseconds(),
	}, nil
}

// Restart stops then starts.
func (n *NopProcessManager) Restart(ctx context.Context, instanceID int64) error {
	n.mu.Lock()
	if st, ok := n.states[instanceID]; ok {
		st.State = StateRestarting
		n.restarts[instanceID]++
	}
	n.mu.Unlock()

	if _, err := n.Stop(ctx, instanceID, false); err != nil {
		return err
	}
	return n.Start(ctx, instanceID)
}

// State returns the current state, if the instance is known.
func (n *NopProcessManager) State(instanceID int64) (StateInfo, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	st, ok := n.states[instanceID]
	if !ok {
		return StateInfo{State: StateUnknown, OnlinePlayers: -1}, false
	}
	cp := *st
	cp.Restarts = n.restarts[instanceID]
	return cp, true
}

// Metrics returns synthetic metrics marked unavailable.
func (n *NopProcessManager) Metrics(instanceID int64) (MetricsInfo, error) {
	n.mu.Lock()
	st, ok := n.states[instanceID]
	startedAt, hasStart := n.started[instanceID]
	restarts := n.restarts[instanceID]
	n.mu.Unlock()

	m := MetricsInfo{
		InstanceID:  instanceID,
		Restarts:    restarts,
		Available:   false,
		Note:        "no-op process manager: no process is running, so /proc metrics are unavailable",
		CollectedAt: n.now().UTC(),
	}
	if !ok {
		return m, nil
	}
	m.LastExitCode = st.ExitCode
	if st.State == StateRunning && hasStart {
		m.UptimeSeconds = int64(n.now().Sub(startedAt).Seconds())
		if m.UptimeSeconds < 0 {
			m.UptimeSeconds = 0
		}
	}
	return m, nil
}

// SendCommand echoes the command onto the log feed.
//
// It applies the same validation the supervisor would, so a bad command is
// rejected with ErrCommandRejected regardless of which layer catches it.
func (n *NopProcessManager) SendCommand(_ context.Context, instanceID int64, line string) error {
	clean, err := ValidateCommand(line)
	if err != nil {
		return err
	}
	n.mu.Lock()
	st, ok := n.states[instanceID]
	running := ok && (st.State == StateRunning || st.State == StateStarting)
	n.mu.Unlock()

	if !running {
		return fmt.Errorf("%w: instance %d is not running", ErrConflict, instanceID)
	}
	n.emit(instanceID, "stdout", "[scnetm] no-op process manager received command: "+clean)
	return nil
}

// Subscribe returns a live feed preloaded with the buffer's history.
func (n *NopProcessManager) Subscribe(_ context.Context, instanceID int64, replay int, raw bool) (LogSubscription, error) {
	sub := &nopSubscription{
		id:     instanceID,
		lines:  make(chan LogLine, 256),
		raw:    raw,
		closed: make(chan struct{}),
	}

	n.mu.Lock()
	if replay > 0 {
		hist := n.history[instanceID]
		if replay > len(hist) {
			replay = len(hist)
		}
		for _, l := range hist[len(hist)-replay:] {
			l.Raw = raw
			sub.replay = append(sub.replay, l)
		}
	}
	if n.subs[instanceID] == nil {
		n.subs[instanceID] = make(map[*nopSubscription]struct{})
	}
	n.subs[instanceID][sub] = struct{}{}
	n.mu.Unlock()

	return sub, nil
}

// emit appends a line to the ring buffer and fans it out to subscribers.
func (n *NopProcessManager) emit(instanceID int64, stream, text string) {
	n.mu.Lock()
	n.seq[instanceID]++
	line := LogLine{
		InstanceID: instanceID,
		Seq:        n.seq[instanceID],
		Timestamp:  n.now().UTC(),
		Stream:     stream,
		Text:       text,
	}
	hist := append(n.history[instanceID], line)
	const maxHistory = 2000
	if len(hist) > maxHistory {
		hist = hist[len(hist)-maxHistory:]
	}
	n.history[instanceID] = hist

	subs := make([]*nopSubscription, 0, len(n.subs[instanceID]))
	for s := range n.subs[instanceID] {
		subs = append(subs, s)
	}
	n.mu.Unlock()

	for _, s := range subs {
		s.push(line)
	}
}

// EnsureInstance pre-registers an instance id so State returns ok=true before
// the first Start. Handlers do not need this (they fall back to Unknown), but
// it makes tests and demos tidier.
func (n *NopProcessManager) EnsureInstance(instanceID int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.states[instanceID]; !ok {
		n.states[instanceID] = &StateInfo{State: StateCreated, OnlinePlayers: -1}
	}
}

// Close releases every subscription. It is called on server shutdown.
func (n *NopProcessManager) Close() {
	n.mu.Lock()
	all := make([]*nopSubscription, 0)
	for _, m := range n.subs {
		for s := range m {
			all = append(all, s)
		}
	}
	n.subs = make(map[int64]map[*nopSubscription]struct{})
	n.mu.Unlock()
	for _, s := range all {
		s.close()
	}
}

// setClockForTest swaps the clock.
func (n *NopProcessManager) setClockForTest(now func() time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.now = now
}

// nopSubscription is the LogSubscription returned by NopProcessManager.
type nopSubscription struct {
	id     int64
	raw    bool
	lines  chan LogLine
	replay []LogLine

	closeOnce sync.Once
	closed    chan struct{}
}

// Lines returns the live channel.
func (s *nopSubscription) Lines() <-chan LogLine { return s.lines }

// History returns the replay buffer.
func (s *nopSubscription) History(n int) []LogLine {
	if n <= 0 || n >= len(s.replay) {
		return s.replay
	}
	return s.replay[len(s.replay)-n:]
}

// Close stops the feed. Safe to call repeatedly.
func (s *nopSubscription) Close() error {
	s.close()
	return nil
}

func (s *nopSubscription) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		close(s.lines)
	})
}

// push delivers a line, dropping it if the consumer is hopelessly behind, and
// never blocking: a slow WebSocket client must not stall the supervisor.
func (s *nopSubscription) push(l LogLine) {
	l.Raw = s.raw
	select {
	case <-s.closed:
		return
	default:
	}
	select {
	case s.lines <- l:
	case <-s.closed:
	default:
		// Buffer full: drop. A production supervisor would count this and
		// expose a "lines dropped" metric.
	}
}
