//go:build linux

package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file covers the public surface the API/WebSocket layer will actually
// call, plus the error paths that are easy to leave untested. It exists so that
// an uncovered line means "deliberately defensive", never "new API nobody
// exercised".

// TestPublicAccessors exercises the read-only accessors in one pass against a
// live instance, which is how the API layer uses them.
func TestPublicAccessors(t *testing.T) {
	dir := t.TempDir()
	r := startRunner(t, helperOpts(t, "ready", func(o *Options) {
		o.ID = 77
		o.Dir = dir
		o.LogDir = filepath.Join(dir, "logs")
		o.Rows, o.Cols = 50, 200
		o.Term = "xterm"
		o.DOTNETRoot = "/opt/dotnet"
		o.TZ = "UTC"
		// Append, do not replace: the helper mode env var must survive.
		o.Env = append(append([]string{}, o.Env...), "EXTRA=1")
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	if r.ID() != 77 {
		t.Errorf("ID = %d, want 77", r.ID())
	}
	o := r.Options()
	if o.ID != 77 || o.Term != "xterm" || o.Rows != 50 || o.Cols != 200 {
		t.Errorf("Options() = %+v, want the configured values", o)
	}
	if !r.Ready() {
		t.Error("Ready() = false after WaitReady")
	}
	if v := r.StateVersion(); v == 0 {
		t.Error("StateVersion() = 0")
	}
	if got := r.EffectiveColorMode(); got != ColorModeBasic {
		t.Errorf("EffectiveColorMode() = %q, want basic (no PTY here)", got)
	}
	if got := r.TermMode(); got != string(methodPipe) {
		t.Errorf("TermMode() = %q, want pipe", got)
	}
	if r.Starts() < 1 {
		t.Errorf("Starts() = %d, want >= 1", r.Starts())
	}
	degraded, reason := r.Degraded()
	if r.opts.wantsPTY() && !degraded {
		t.Error("Degraded() = false, but this host cannot allocate a PTY")
	}
	if degraded && reason == "" {
		t.Error("Degraded() reported degradation without a reason; it must never be silent")
	}

	// SetSize in pipe mode is explicitly unsupported, with a typed error.
	if err := r.SetSize(30, 100); !isErr(err, ErrNoCommandChannel) {
		t.Errorf("SetSize in pipe mode = %v, want ErrNoCommandChannel", err)
	}
	// In pipe mode every SetSize is ErrNoCommandChannel, including a zero size:
	// the pipe short-circuits before the size is validated.
	if err := r.SetSize(0, 0); !isErr(err, ErrNoCommandChannel) {
		t.Errorf("SetSize(0,0) in pipe mode = %v, want ErrNoCommandChannel", err)
	}

	// RingLen is what the UI uses to show the replay depth.
	if err := r.SendCommand("help"); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	r.mu.RLock()
	pipe := r.pipe
	r.mu.RUnlock()
	if pipe == nil {
		t.Fatal("no pipe while running")
	}
	waitFor(t, 3*time.Second, "ring to fill", func() bool { return pipe.RingLen() > 0 })

	// Stats feeds the diagnostics endpoint.
	lines, dropped, subs := pipe.Stats()
	if lines == 0 {
		t.Error("Stats: lines = 0 for a running instance")
	}
	_ = dropped
	if subs != 0 {
		t.Errorf("Stats: subscribers = %d, want 0", subs)
	}

	if res, err := r.Stop(context.Background()); err != nil || !res.Graceful {
		t.Fatalf("Stop: res=%+v err=%v", res, err)
	}
	if last := r.LastStop(); last.Method == "" || last.Method == "none" {
		t.Errorf("LastStop() = %+v, want the recorded method", last)
	}
	if r.Ready() {
		t.Error("Ready() = true after stop")
	}
}

// TestSubscriptionRawFlag covers both wire formats the console toggle needs.
func TestSubscriptionRawFlag(t *testing.T) {
	r := startRunner(t, helperOpts(t, "ready"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
		t.Fatalf("wait for Running: %v", err)
	}

	rawSub, err := r.Subscribe(ctx, 0, true)
	if err != nil {
		t.Fatalf("Subscribe(raw): %v", err)
	}
	defer rawSub.Close()
	if !rawSub.Raw() {
		t.Error("Raw() = false for a raw subscription")
	}

	plainSub, err := r.Subscribe(ctx, 0, false)
	if err != nil {
		t.Fatalf("Subscribe(plain): %v", err)
	}
	defer plainSub.Close()
	if plainSub.Raw() {
		t.Error("Raw() = true for a plain subscription")
	}

	if err := r.SendCommand("help"); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}

	// The plain subscription must never carry ANSI bytes on the wire.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case rec := <-plainSub.Ch():
			if strings.Contains(rec.Raw, "\x1b") {
				t.Errorf("plain subscription carried ANSI in Raw: %q", rec.Raw)
			}
			if strings.Contains(rec.Plain, "\x1b") {
				t.Errorf("Plain contains ANSI: %q", rec.Plain)
			}
			if strings.Contains(rec.Plain, "help") {
				return
			}
		case <-deadline:
			t.Fatal("plain subscription never saw the command echo")
		}
	}
}

// TestReplayWithCancelledContext covers the context guard.
func TestReplayWithCancelledContext(t *testing.T) {
	r := startRunner(t, helperOpts(t, "ready"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := r.Replay(ctx, 10, true); got != nil {
		t.Errorf("Replay with a cancelled context = %v, want nil", got)
	}
}

// TestSubscribeWithCancelledContext covers the same guard on Subscribe.
func TestSubscribeWithCancelledContext(t *testing.T) {
	r := startRunner(t, helperOpts(t, "ready"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
		t.Fatalf("wait for Running: %v", err)
	}
	cancelled, ccancel := context.WithCancel(context.Background())
	ccancel()
	if _, err := r.Subscribe(cancelled, 0, true); !errors.Is(err, context.Canceled) {
		t.Errorf("Subscribe with a cancelled context = %v, want context.Canceled", err)
	}
}

// TestWaitContextTimesOut covers WaitContext's cancellation path.
func TestWaitContextTimesOut(t *testing.T) {
	r := startRunner(t, helperOpts(t, "ready"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
		t.Fatalf("wait for Running: %v", err)
	}
	// The process is still alive, so a short WaitContext must time out.
	short, scancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer scancel()
	if err := r.WaitContext(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("WaitContext = %v, want DeadlineExceeded", err)
	}
	// Wait() blocks until the process actually ends.
	done := make(chan struct{})
	go func() { r.Wait(); close(done) }()
	if err := r.SendCommand("/stop"); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Wait() did not return after the child exited")
	}
}

// TestSetFactoryAndRegister covers the two manager hooks the restore path uses.
func TestSetFactoryAndRegister(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())

	// SetFactory lets a caller wrap runner construction (used by the API layer to
	// attach its own event hook).
	used := false
	m.SetFactory(func(o Options) (*Runner, error) {
		used = true
		return NewRunnerWithSink(o, m.Bus().Publish)
	})

	r, err := NewRunner(helperOpts(t, "ready", func(o *Options) { o.ID = 5 }))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if err := m.Register(r); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got, ok := m.Get(5); !ok || got != r {
		t.Error("Get after Register did not return the registered runner")
	}
	// Duplicate registration is rejected.
	if err := m.Register(r); err == nil {
		t.Error("double Register succeeded")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := m.Add(ctx, helperOpts(t, "ready", func(o *Options) { o.ID = 6 }), false); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !used {
		t.Error("SetFactory replacement was not used by Add")
	}
	if m.Bus() == nil {
		t.Error("Bus() returned nil")
	}
}

// TestManagerAddStartFailureKeepsRegistration proves a failed start still leaves
// the instance visible, which is what lets the UI show the Failed state.
func TestManagerAddStartFailureKeepsRegistration(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	o := helperOpts(t, "ready", func(o *Options) {
		o.ID = 9
		o.Executable = "/nonexistent/dotnet"
	})
	r, err := m.Add(ctx, o, true)
	if err == nil {
		t.Fatal("Add with a bad executable returned nil error")
	}
	if r == nil {
		t.Fatal("Add returned no runner for a failed start")
	}
	if got, ok := m.Get(9); !ok || got != r {
		t.Error("the failed instance was not kept registered")
	}
	if r.State() != StateFailed {
		t.Errorf("state = %s, want failed", r.State())
	}
}

// TestRotatingWriterErrorPaths covers the error branches of the disk writer.
func TestRotatingWriterErrorPaths(t *testing.T) {
	dir := t.TempDir()
	w, err := newRotatingWriter(dir, ".log")
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	if got, want := w.Path(), filepath.Join(dir, dayKey(time.Now())+".log"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Writing after Close is a typed error, not a panic.
	if _, err := w.WriteLine("x"); !isErr(err, ErrClosed) {
		t.Errorf("WriteLine after Close = %v, want ErrClosed", err)
	}
	// Double Close is safe, and Flush after Close is a no-op.
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Errorf("Flush after Close = %v, want nil", err)
	}

	// An unwritable directory must fail loudly at construction.
	if _, err := newRotatingWriter(filepath.Join(dir, "nope", "deeper"), ".log"); err == nil {
		// This succeeds on a writable parent; force a real failure instead.
		bad := filepath.Join(dir, "afile")
		if werr := os.WriteFile(bad, []byte("x"), 0o644); werr != nil {
			t.Fatalf("setup: %v", werr)
		}
		if _, err := newRotatingWriter(filepath.Join(bad, "sub"), ".log"); err == nil {
			t.Error("newRotatingWriter under a regular file returned nil error")
		}
	}
}

// TestErrorTypes covers the error rendering helpers the API maps to HTTP codes.
func TestErrorTypes(t *testing.T) {
	ee := &ExitError{Code: 3}
	if got := ee.Error(); !strings.Contains(got, "code 3") {
		t.Errorf("ExitError.Error() = %q", got)
	}
	sig := &ExitError{Code: 137, Signal: "SIGKILL"}
	if got := sig.Error(); !strings.Contains(got, "SIGKILL") {
		t.Errorf("ExitError.Error() = %q", got)
	}
	var nilErr *ExitError
	if got := nilErr.Error(); got == "" {
		t.Error("nil ExitError.Error() must not panic or return empty")
	}

	se := &startError{Stage: "exec", Err: errors.New("boom")}
	if got := se.Error(); !strings.Contains(got, "exec") || !strings.Contains(got, "boom") {
		t.Errorf("startError.Error() = %q", got)
	}
	if !errors.Is(se.Unwrap(), se.Err) {
		t.Error("startError.Unwrap did not return the wrapped error")
	}
	if !errors.Is(se, se.Err) {
		t.Error("errors.Is did not traverse startError")
	}
}

// TestIsPTYUnavailableAndSignalGroup covers the small Linux helpers.
func TestIsPTYUnavailableAndSignalGroup(t *testing.T) {
	if isPTYUnavailable(nil) {
		t.Error("isPTYUnavailable(nil) = true")
	}
	if !isPTYUnavailable(os.ErrPermission) {
		t.Error("isPTYUnavailable(EPERM) = false")
	}
	if isPTYUnavailable(errors.New("some unrelated failure")) {
		t.Error("isPTYUnavailable matched an unrelated error")
	}
	// A non-positive pid is a programming error, not a crash.
	if err := signalGroup(0, syscallSIGTERM); err == nil {
		t.Error("signalGroup(0) returned nil error")
	}
	if err := signalGroup(-5, syscallSIGTERM); err == nil {
		t.Error("signalGroup(-5) returned nil error")
	}
	// An already-dead pid is a no-op (the desired state already holds).
	if err := signalGroup(1<<30, syscallSIGTERM); err != nil {
		t.Errorf("signalGroup on a dead pid = %v, want nil", err)
	}
}

// TestWaitExitCode covers the exec error classification used for exit codes.
func TestWaitExitCode(t *testing.T) {
	if code, sig := waitExitCode(nil); code != 0 || sig != "" {
		t.Errorf("waitExitCode(nil) = %d, %q; want 0, empty", code, sig)
	}
	if code, _ := waitExitCode(errors.New("not an ExitError")); code != -1 {
		t.Errorf("waitExitCode(plain error) = %d, want -1", code)
	}
}

// TestSampleMetricsWrapperAndErrIsExited covers the two convenience helpers.
func TestSampleMetricsWrapperAndErrIsExited(t *testing.T) {
	r, err := NewRunner(helperOpts(t, "ready"))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if _, err := SampleMetrics(r); !isErr(err, ErrNotRunning) {
		t.Errorf("SampleMetrics on a stopped runner = %v, want ErrNotRunning", err)
	}
	if !errIsExited(ErrNotRunning) || !errIsExited(ErrProcessExited) {
		t.Error("errIsExited did not recognise the exited/not-running sentinels")
	}
	if errIsExited(errors.New("other")) {
		t.Error("errIsExited matched an unrelated error")
	}
}

// TestValidateOptionsEdgeCases covers the remaining validation branches.
func TestValidateOptionsEdgeCases(t *testing.T) {
	base := Options{Dir: t.TempDir(), Executable: "/bin/true"}

	o := base
	o.RingLines = -1
	if err := o.Validate(); !isErr(err, ErrInvalidOptions) {
		t.Errorf("negative RingLines = %v, want ErrInvalidOptions", err)
	}
	// Every documented colour mode must validate, including the empty default.
	for _, mode := range []string{ColorModeAuto, ColorModeBasic, ColorModeEnhanced, ""} {
		o := base
		o.ColorMode = mode
		if err := o.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v", mode, err)
		}
	}
}

// TestCommandValidationBoundaries pins the exact boundary of the length guard.
func TestCommandValidationBoundaries(t *testing.T) {
	if err := ValidateCommand(strings.Repeat("x", MaxCommandBytes-1)); err != nil {
		t.Errorf("command one byte under the limit rejected: %v", err)
	}
	if err := ValidateCommand(strings.Repeat("x", MaxCommandBytes+1)); !isErr(err, ErrCommandTooLong) {
		t.Errorf("command one byte over the limit = %v, want ErrCommandTooLong", err)
	}
	// Multi-byte UTF-8 is measured in bytes, which is what the wire sees.
	cn := strings.Repeat("中", MaxCommandBytes/3+1)
	if err := ValidateCommand(cn); !isErr(err, ErrCommandTooLong) {
		t.Errorf("oversize UTF-8 command = %v, want ErrCommandTooLong", err)
	}
	// A perfectly ordinary Chinese command must pass.
	if err := ValidateCommand("/say 大家好"); err != nil {
		t.Errorf("valid Chinese command rejected: %v", err)
	}
}
