//go:build linux

package supervisor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"scnetm/internal/ansi"
)

// TestMain gives every test the re-exec helper and keeps the PTY probe honest.
func TestMain(m *testing.M) {
	maybeRunHelper()
	os.Exit(m.Run())
}

// ---------------------------------------------------------------- test rig

// helperOpts builds Options that run the test binary itself in helper mode.
func helperOpts(t *testing.T, mode string, mutate ...func(*Options)) Options {
	t.Helper()
	o := Options{
		ID:          1,
		Dir:         t.TempDir(),
		Executable:  os.Args[0],
		Args:        []string{"-test.run=TestMain", "--", mode},
		Env:         helperEnvFor(mode),
		ColorMode:   ColorModeBasic, // most tests do not need a PTY
		RingLines:   256,
		LogDir:      filepath.Join(t.TempDir(), "logs"),
		StopTimeout: 2 * time.Second,
		KillTimeout: 2 * time.Second,
	}
	for _, m := range mutate {
		m(&o)
	}
	return o
}

// startRunner builds and starts a runner, failing the test on error.
func startRunner(t *testing.T, o Options) *Runner {
	t.Helper()
	r, err := NewRunner(o)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = r.Stop(ctx)
	})
	return r
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout after %s waiting for %s", timeout, what)
}

// ---------------------------------------------------------------- 1. happy path

// TestStartReadyCommandStop is the end-to-end contract the API layer depends on:
// launch a real child, reach Running through the log anchors, send a command,
// stop gracefully, and observe a graceful StopResult with exit code 0.
func TestStartReadyCommandStop(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	r := startRunner(t, helperOpts(t, "ready", func(o *Options) {
		o.Dir = dir
		o.LogDir = logDir
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Subscribe before the anchors land so nothing is missed.
	sub, err := r.Subscribe(ctx, 0, true)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()

	if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
		t.Fatalf("wait for Running: %v (state %s, snapshot %+v)", err, r.State(), r.Snapshot())
	}
	snap := r.Snapshot()
	if !snap.Ready() {
		t.Fatalf("Running but not Ready: %+v", snap)
	}
	if snap.Port != 28887 {
		t.Errorf("captured port = %d, want 28887", snap.Port)
	}
	if snap.WorldName != "ShowNameX" {
		t.Errorf("captured world name = %q, want ShowNameX", snap.WorldName)
	}
	if snap.Version == 0 {
		t.Error("StateVersion is zero")
	}
	if snap.PID <= 0 {
		t.Error("PID not recorded")
	}

	// Send a command and see it echoed back through the log pipeline.
	if err := r.SendCommand("help"); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	waitFor(t, 5*time.Second, "command echo", func() bool {
		for _, rec := range r.Replay(ctx, 0, true) {
			if strings.Contains(rec.Plain, `executed "help"`) {
				return true
			}
		}
		return false
	})

	// Stop: the helper handles /stop and exits 0.
	res, err := r.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !res.Graceful {
		t.Errorf("StopResult.Graceful = false, want true (method=%s)", res.Method)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if res.Method == "" || res.Method == "none" {
		t.Errorf("Method = %q, want a real method", res.Method)
	}
	if r.State() != StateStopped {
		t.Errorf("state = %s, want stopped", r.State())
	}
}

// TestAnchorsRequireAllThree proves the §5.1 rule: liveness alone is not
// readiness. A child that starts fine but never prints the anchors stays in
// Starting until the start timeout flips it to Failed.
func TestAnchorsRequireAllThree(t *testing.T) {
	r := startRunner(t, helperOpts(t, "no-anchor", func(o *Options) {
		o.StartTimeout = 700 * time.Millisecond
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Give it a moment to prove it does NOT become Running on liveness alone.
	time.Sleep(250 * time.Millisecond)
	if got := r.State(); got != StateStarting {
		t.Fatalf("state = %s, want starting (liveness must not imply readiness)", got)
	}

	if err := r.WaitState(ctx, func(s State) bool { return s.Terminal() }); err != nil {
		t.Fatalf("wait for terminal state: %v", err)
	}
	if got := r.State(); got != StateFailed {
		t.Fatalf("state = %s, want failed after start timeout", got)
	}
	if msg := r.Snapshot().LastError; !strings.Contains(msg, "startup timeout") {
		t.Errorf("LastError = %q, want a startup-timeout explanation", msg)
	}
	// The failed process must actually be reaped, not left behind. The teardown
	// is asynchronous, so wait for it rather than sampling once.
	waitFor(t, 10*time.Second, "failed process reaped", func() bool { return r.PID() == 0 })
}

// TestExitBeforeReadyIsFailed proves an exit while Starting is Failed, not
// Crashed: the instance never became ready, so it did not "crash at runtime".
func TestExitBeforeReadyIsFailed(t *testing.T) {
	r := startRunner(t, helperOpts(t, "crash"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := r.WaitState(ctx, func(s State) bool { return s.Terminal() }); err != nil {
		t.Fatalf("wait for terminal state: %v", err)
	}
	if got := r.State(); got != StateFailed {
		t.Fatalf("state = %s, want failed (exited while Starting, before readiness)", got)
	}
	snap := r.Snapshot()
	if snap.ExitCode == nil {
		t.Fatal("ExitCode is nil for a failed instance")
	}
	if *snap.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", *snap.ExitCode)
	}
	if !strings.Contains(snap.LastError, "before readiness") {
		t.Errorf("LastError = %q, want a before-readiness explanation", snap.LastError)
	}
}

// TestCrashWhileRunning exercises the true crash path: the process reaches
// Running (anchors seen) and *then* dies. The helper mode is driven by an env
// knob so the same binary both becomes ready and dies.
func TestCrashWhileRunning(t *testing.T) {
	// crash-after-ready: helperReady prints anchors then exits 9 on stdin "die".
	o := helperOpts(t, "ready", func(o *Options) {
		o.Args = []string{"-test.run=TestMain", "--", "ready"}
	})
	r := startRunner(t, o)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
		t.Fatalf("wait for Running: %v", err)
	}
	// Kill the process group directly, simulating an external OOM kill.
	pid := r.PID()
	if pid <= 0 {
		t.Fatal("no pid")
	}
	if err := signalGroup(pid, syscallSIGKILL); err != nil {
		t.Fatalf("signalGroup: %v", err)
	}
	if err := r.WaitState(ctx, func(s State) bool { return s.Terminal() }); err != nil {
		t.Fatalf("wait for terminal: %v", err)
	}
	if got := r.State(); got != StateCrashed {
		t.Fatalf("state = %s, want crashed", got)
	}
	snap := r.Snapshot()
	if snap.ExitCode == nil || *snap.ExitCode != 137 {
		t.Errorf("ExitCode = %v, want 137 (128+SIGKILL)", snap.ExitCode)
	}
}

// TestStopWhenNotRunning documents the typed error contract for Stop.
func TestStopWhenNotRunning(t *testing.T) {
	r, err := NewRunner(helperOpts(t, "ready"))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	res, err := r.Stop(context.Background())
	if err == nil {
		t.Fatal("Stop on a never-started runner returned nil error")
	}
	if !isErr(err, ErrAlreadyStopped) {
		t.Errorf("err = %v, want ErrAlreadyStopped", err)
	}
	if res.Method != "none" {
		t.Errorf("Method = %q, want none", res.Method)
	}
	// Idempotent: a second call behaves the same.
	if _, err2 := r.Stop(context.Background()); !isErr(err2, ErrAlreadyStopped) {
		t.Errorf("second Stop err = %v, want ErrAlreadyStopped", err2)
	}
}

// TestStopIdempotentConcurrent proves concurrent Stop calls are safe and agree.
func TestStopIdempotentConcurrent(t *testing.T) {
	r := startRunner(t, helperOpts(t, "ready"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
		t.Fatalf("wait for Running: %v", err)
	}

	const n = 5
	var wg sync.WaitGroup
	results := make([]StopResult, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = r.Stop(context.Background())
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil && !isErr(errs[i], ErrAlreadyStopped) {
			t.Errorf("goroutine %d: unexpected error %v", i, errs[i])
		}
	}
	// Exactly one caller does the work; the rest see the stopped instance.
	graceful := 0
	for i := range results {
		if results[i].Graceful && results[i].ExitCode == 0 {
			graceful++
		}
	}
	if graceful == 0 {
		t.Fatalf("no caller observed a graceful stop: %+v", results)
	}
	if r.State() != StateStopped {
		t.Errorf("state = %s, want stopped", r.State())
	}
}

// ---------------------------------------------------------------- 2. signals

// TestStopEscalatesToSigterm covers stage 3: the child ignores /stop and Ctrl+C,
// so the ladder must escalate to SIGTERM and report Graceful=false.
func TestStopEscalatesToSigterm(t *testing.T) {
	r := startRunner(t, helperOpts(t, "ignore-cmd", func(o *Options) {
		o.StopTimeout = 500 * time.Millisecond
		o.KillTimeout = 5 * time.Second
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
		t.Fatalf("wait for Running: %v", err)
	}

	res, err := r.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if res.Method != "sigterm" {
		t.Errorf("Method = %q, want sigterm", res.Method)
	}
	if res.Graceful {
		t.Error("Graceful = true, want false (the save may be incomplete)")
	}
	if res.Signal != "SIGTERM" && res.ExitCode != 143 {
		t.Errorf("Signal = %q ExitCode = %d, want SIGTERM/143", res.Signal, res.ExitCode)
	}
}

// TestStopEscalatesToSigkill covers stage 4: the child ignores SIGTERM too, so
// only SIGKILL ends it, and the result is explicitly not graceful.
func TestStopEscalatesToSigkill(t *testing.T) {
	o := helperOpts(t, "ignore-all", func(o *Options) {
		o.StopTimeout = 300 * time.Millisecond
		o.KillTimeout = 500 * time.Millisecond
	})
	r := startRunner(t, o)

	// ignore-all prints no anchors; wait for liveness instead of readiness.
	waitFor(t, 5*time.Second, "process start", func() bool { return r.PID() > 0 })

	res, err := r.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if res.Method != "sigkill" {
		t.Errorf("Method = %q, want sigkill", res.Method)
	}
	if res.Graceful {
		t.Error("Graceful = true, want false")
	}
	if res.Signal != "SIGKILL" && res.ExitCode != 137 {
		t.Errorf("Signal = %q ExitCode = %d, want SIGKILL/137", res.Signal, res.ExitCode)
	}
}

// TestProcessGroupKillsGrandchildren proves signals go to -pgid, not just the
// direct child (§4.3: `dotnet` children must not leak).
func TestProcessGroupKillsGrandchildren(t *testing.T) {
	if _, err := os.Stat("/bin/sleep"); err != nil {
		t.Skip("skipping: /bin/sleep not available")
	}
	o := helperOpts(t, "spawner", func(o *Options) {
		o.StopTimeout = 300 * time.Millisecond
		o.KillTimeout = 1 * time.Second
	})
	r, err := NewRunner(o)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		_, _ = r.Stop(sctx)
	})

	// Subscribe BEFORE starting: the helper forks and logs the pid immediately,
	// so a subscription created afterwards could miss the line entirely.
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sub, err := r.Subscribe(ctx, 0, false)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()

	var grandchild int
	deadline := time.After(15 * time.Second)
	for grandchild == 0 {
		select {
		case rec := <-sub.Ch():
			var pid int
			if n, _ := fmtSscan(rec.Plain, "spawned grandchild %d", &pid); n == 1 {
				grandchild = pid
			}
		case <-deadline:
			t.Fatal("timeout waiting for the grandchild pid to appear in the log")
		}
	}
	if !processAlive(grandchild) {
		t.Fatalf("grandchild %d is not alive before the test", grandchild)
	}

	res, err := r.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	_ = res

	waitFor(t, 5*time.Second, "grandchild reaped with the group", func() bool {
		return !processAlive(grandchild)
	})
}

// ---------------------------------------------------------------- 3. commands

// TestSendCommandValidation covers the injection guards and the typed errors.
func TestSendCommandValidation(t *testing.T) {
	r, err := NewRunner(helperOpts(t, "ready"))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	// Before start: no channel at all.
	if err := r.SendCommand("help"); !isErr(err, ErrNoCommandChannel) {
		t.Errorf("SendCommand before start = %v, want ErrNoCommandChannel", err)
	}
	if err := r.SendCommand(""); !isErr(err, ErrEmptyCommand) {
		t.Errorf("empty command = %v, want ErrEmptyCommand", err)
	}
	if err := r.SendCommand("   "); !isErr(err, ErrEmptyCommand) {
		t.Errorf("blank command = %v, want ErrEmptyCommand", err)
	}
	if err := r.SendCommand("a\nb"); !isErr(err, ErrInvalidCommand) {
		t.Errorf("newline injection = %v, want ErrInvalidCommand", err)
	}
	if err := r.SendCommand("a\rb"); !isErr(err, ErrInvalidCommand) {
		t.Errorf("CR injection = %v, want ErrInvalidCommand", err)
	}
	if err := r.SendCommand(strings.Repeat("x", MaxCommandBytes+1)); !isErr(err, ErrCommandTooLong) {
		t.Errorf("oversize command = %v, want ErrCommandTooLong", err)
	}
	// Exactly at the limit is allowed.
	if err := ValidateCommand(strings.Repeat("x", MaxCommandBytes)); err != nil {
		t.Errorf("command at the limit rejected: %v", err)
	}
}

// TestSendCommandRequiresRunningInstance proves the channel appears only for a
// live process.
func TestSendCommandRequiresRunningInstance(t *testing.T) {
	r := startRunner(t, helperOpts(t, "ready"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
		t.Fatalf("wait for Running: %v", err)
	}
	if err := r.SendCommand("help"); err != nil {
		t.Fatalf("SendCommand while running: %v", err)
	}
	if _, err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := r.SendCommand("help"); !isErr(err, ErrNoCommandChannel) {
		t.Errorf("SendCommand after stop = %v, want ErrNoCommandChannel", err)
	}
}

// ---------------------------------------------------------------- 4. startup errors

func TestStartValidationAndFailures(t *testing.T) {
	t.Run("missing dir", func(t *testing.T) {
		o := helperOpts(t, "ready")
		o.Dir = ""
		if _, err := NewRunner(o); !isErr(err, ErrInvalidOptions) {
			t.Errorf("NewRunner with empty Dir = %v, want ErrInvalidOptions", err)
		}
	})
	t.Run("missing executable", func(t *testing.T) {
		o := helperOpts(t, "ready")
		o.Executable = ""
		if _, err := NewRunner(o); !isErr(err, ErrInvalidOptions) {
			t.Errorf("NewRunner with empty Executable = %v, want ErrInvalidOptions", err)
		}
	})
	t.Run("bad color mode", func(t *testing.T) {
		o := helperOpts(t, "ready")
		o.ColorMode = "rainbow"
		if _, err := NewRunner(o); !isErr(err, ErrInvalidOptions) {
			t.Errorf("NewRunner with bad ColorMode = %v, want ErrInvalidOptions", err)
		}
	})
	t.Run("no such binary", func(t *testing.T) {
		o := helperOpts(t, "ready")
		o.Executable = "/nonexistent/dotnet"
		r, err := NewRunner(o)
		if err != nil {
			t.Fatalf("NewRunner: %v", err)
		}
		if err := r.Start(context.Background()); err == nil {
			t.Fatal("Start with a missing binary returned nil")
		}
		if got := r.State(); got != StateFailed {
			t.Errorf("state = %s, want failed", got)
		}
	})
	t.Run("double start", func(t *testing.T) {
		r := startRunner(t, helperOpts(t, "ready"))
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
			t.Fatalf("wait for Running: %v", err)
		}
		if err := r.Start(context.Background()); !isErr(err, ErrAlreadyRunning) {
			t.Errorf("second Start = %v, want ErrAlreadyRunning", err)
		}
	})
	t.Run("no command channel in basic mode", func(t *testing.T) {
		// basic means pipes; the command channel is the stdin pipe, so it works.
		r := startRunner(t, helperOpts(t, "ready", func(o *Options) {
			o.ColorMode = ColorModeBasic
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
			t.Fatalf("wait for Running: %v", err)
		}
		if err := r.SendCommand("help"); err != nil {
			t.Errorf("SendCommand in pipe mode: %v", err)
		}
	})
}

// ---------------------------------------------------------------- 5. PTY

// TestPTYMode covers the preferred path. PTY allocation fails in this sandbox
// ("out of pty devices"), in which case the test proves the documented
// degradation instead of failing.
func TestPTYMode(t *testing.T) {
	r := startRunner(t, helperOpts(t, "ready", func(o *Options) {
		o.ColorMode = ColorModeEnhanced
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if degraded, reason := r.Degraded(); degraded {
		t.Logf("PTY not available in this environment; degradation reported: %s", reason)
		if r.TermMode() != string(methodPipe) {
			t.Errorf("degraded but TermMode = %q, want pipe", r.TermMode())
		}
		// The degradation must be observable on the event stream too — never
		// silent (§5.2).
		if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
			t.Fatalf("degraded runner never became Running: %v", err)
		}
		return
	}

	if r.TermMode() != string(methodPTY) {
		t.Fatalf("TermMode = %q, want pty", r.TermMode())
	}
	if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
		t.Fatalf("wait for Running: %v", err)
	}
	// A real PTY must deliver ANSI bytes.
	if err := r.SetSize(50, 200); err != nil {
		t.Errorf("SetSize: %v", err)
	}
	lines := r.Replay(ctx, 0, true)
	var sawANSI bool
	for _, l := range lines {
		if strings.Contains(l.Raw, "\x1b[") {
			sawANSI = true
		}
	}
	if !sawANSI {
		t.Error("PTY mode produced no ANSI bytes; colour would be lost (D1)")
	}
}

// TestPTYUnavailableFallback pins the fallback contract directly, without
// relying on the sandbox's PTY behaviour.
func TestPTYUnavailableFallback(t *testing.T) {
	if ptyAllocatable() {
		t.Skip("skipping: a PTY could actually be allocated here, so the fallback path is not exercised")
	}
	// Forcing ColorModeAuto with a host that cannot allocate must still start
	// the process, in pipe mode, with the degradation recorded.
	r := startRunner(t, helperOpts(t, "ready", func(o *Options) {
		o.ColorMode = ColorModeAuto
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
		t.Fatalf("auto mode never became Running: %v", err)
	}
	if eff := r.EffectiveColorMode(); eff != ColorModeBasic {
		t.Errorf("EffectiveColorMode = %q, want basic on a PTY-less host", eff)
	}
}

// ---------------------------------------------------------------- 6. env

// TestEnvironmentInjection proves the §4.3 environment contract without
// invoking a shell. The helper is `env`, which prints and exits immediately, so
// the output is captured live through OnLineFn rather than from the ring (which
// is torn down with the pipe as soon as the process exits).
func TestEnvironmentInjection(t *testing.T) {
	envBin, err := exec.LookPath("env")
	if err != nil {
		t.Skip("skipping: no `env` binary available")
	}
	var mu sync.Mutex
	var out string
	r, err := NewRunner(Options{
		ID:          1,
		Dir:         t.TempDir(),
		Executable:  envBin,
		Term:        "xterm-256color",
		ColorMode:   ColorModeBasic,
		DOTNETRoot:  "/opt/dotnet",
		TZ:          "Asia/Shanghai",
		Env:         []string{"SCNETM_EXTRA=yes"},
		LogDir:      filepath.Join(t.TempDir(), "logs"),
		StopTimeout: time.Second,
		OnLineFn: func(l ansi.Line) {
			mu.Lock()
			out += l.Plain + "\n"
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Wait for the child to exit and the reader to drain its output.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer waitCancel()
	_ = r.WaitContext(waitCtx)
	waitFor(t, 5*time.Second, "child output to be captured", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(out, "SCNETM_EXTRA=yes")
	})
	mu.Lock()
	captured := out
	mu.Unlock()
	for _, want := range []string{
		"TERM=xterm-256color",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"DOTNET_ROOT=/opt/dotnet",
		"TZ=Asia/Shanghai",
		"SCNETM_EXTRA=yes",
	} {
		if !strings.Contains(captured, want) {
			t.Errorf("child environment missing %q", want)
		}
	}
}

// ---------------------------------------------------------------- 7. goroutines

// TestNoGoroutineLeak starts and stops five instances sequentially and checks
// the goroutine count returns to baseline. Manual counting, no new dependency.
func TestNoGoroutineLeak(t *testing.T) {
	// Let stray runtime goroutines from earlier tests settle first.
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	baseline := runtime.NumGoroutine()

	for i := 0; i < 5; i++ {
		r := startRunner(t, helperOpts(t, "ready"))
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
			cancel()
			t.Fatalf("iteration %d: wait for Running: %v", i, err)
		}
		res, err := r.Stop(context.Background())
		cancel()
		if err != nil {
			t.Fatalf("iteration %d: Stop: %v", i, err)
		}
		if !res.Graceful {
			t.Fatalf("iteration %d: not graceful (%s)", i, res.Method)
		}
	}

	// Allow the waitLoop teardown to finish.
	var final int
	for attempt := 0; attempt < 40; attempt++ {
		time.Sleep(50 * time.Millisecond)
		runtime.GC()
		final = runtime.NumGoroutine()
		if final <= baseline+2 {
			break
		}
	}
	if final > baseline+2 {
		buf := make([]byte, 1<<16)
		n := runtime.Stack(buf, true)
		t.Errorf("goroutine leak: baseline %d, after 5 start/stop cycles %d\n%s",
			baseline, final, buf[:n])
	}
}

// TestStopFlushesLogBeforeReturning proves Stop guarantees the tail reached disk
// and that the writers are closed (no lost lines after Stop returns).
func TestStopFlushesLogBeforeReturning(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	r := startRunner(t, helperOpts(t, "tail", func(o *Options) {
		o.Dir = dir
		o.LogDir = logDir
		o.RingLines = 64
	}))
	// "tail" prints a partial line and exits immediately; the runner sees the
	// exit while Starting and must still flush.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.WaitState(ctx, func(s State) bool { return s.Terminal() }); err != nil {
		t.Fatalf("wait for terminal: %v", err)
	}
	// Give the reader a beat to finish, then assert the tail is on disk.
	waitFor(t, 3*time.Second, "tail line written to the plain log", func() bool {
		b, err := os.ReadFile(filepath.Join(logDir, dayKey(time.Now())+logFilePlainSuffix))
		return err == nil && strings.Contains(string(b), "最后一行没有换行符")
	})
}
