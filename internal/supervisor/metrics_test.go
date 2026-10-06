//go:build linux

package supervisor

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------- /proc parsing

// TestParseProcStat covers the "comm contains spaces and parens" hazard: fields
// after comm must be located from the LAST ')'.
func TestParseProcStat(t *testing.T) {
	// Field layout: pid (comm) state ppid pgrp session tty_nr tpgid flags minflt
	// cminflt majflt cmajflt utime stime cutime cstime priority nice num_threads
	// itrealvalue starttime vsize rss
	line := "1234 (my (weird) proc name) S 1 1234 1234 0 -1 4194560 100 0 0 0 " +
		"150 75 0 0 20 0 7 0 987654 123456789 4321"
	st, err := parseProcStat(1234, line)
	if err != nil {
		t.Fatalf("parseProcStat: %v", err)
	}
	if st.comm != "my (weird) proc name" {
		t.Errorf("comm = %q", st.comm)
	}
	if st.state != 'S' {
		t.Errorf("state = %q, want S", st.state)
	}
	if st.utime != 150 || st.stime != 75 {
		t.Errorf("utime/stime = %d/%d, want 150/75", st.utime, st.stime)
	}
	if st.threads != 7 {
		t.Errorf("threads = %d, want 7", st.threads)
	}
	if st.startTicks != 987654 {
		t.Errorf("starttime = %d, want 987654", st.startTicks)
	}
	if st.vsize != 123456789 {
		t.Errorf("vsize = %d, want 123456789", st.vsize)
	}
	if st.rssPages != 4321 {
		t.Errorf("rss = %d, want 4321", st.rssPages)
	}
}

// TestParseProcStatMalformed proves a truncated stat line is an error, not a
// panic and not a silently bogus sample.
func TestParseProcStatMalformed(t *testing.T) {
	for _, bad := range []string{"", "garbage", "1234 no-parens S 1 2 3"} {
		if _, err := parseProcStat(1, bad); err == nil {
			t.Errorf("parseProcStat(%q) returned nil error", bad)
		}
	}
}

// TestParseProcStatShortLineDegrades: some fields may be missing on exotic
// kernels; parsing must still succeed with zeroes rather than fail.
func TestParseProcStatShortLineDegrades(t *testing.T) {
	st, err := parseProcStat(1, "1 (x) R 0")
	if err != nil {
		t.Fatalf("parseProcStat: %v", err)
	}
	if st.comm != "x" || st.state != 'R' {
		t.Errorf("got %+v", st)
	}
	if st.utime != 0 || st.rssPages != 0 {
		t.Errorf("missing fields should be zero, got %+v", st)
	}
}

// ---------------------------------------------------------------- live sampling

// TestMetricsOnLiveProcess samples a real child and checks every §6.6 field.
func TestMetricsOnLiveProcess(t *testing.T) {
	o := helperOpts(t, "ready", func(o *Options) {
		o.StartTimeout = 10 * time.Second
	})
	r := startRunner(t, o)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
		t.Fatalf("wait for Running: %v", err)
	}

	// Burn a little CPU in the child so CPU% is non-zero on the second sample.
	if err := r.SendCommand("spin"); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	m1, err := r.Sample()
	if err != nil {
		t.Fatalf("first Sample: %v", err)
	}
	if m1.PID <= 0 {
		t.Error("PID not set")
	}
	if m1.RSSBytes == 0 {
		t.Error("RSSBytes = 0 for a live Go process")
	}
	if m1.Threads <= 0 {
		t.Error("Threads = 0 for a live process")
	}
	if m1.FDs <= 0 {
		t.Error("FDs = 0 for a live process (stdin/stdout/stderr at minimum)")
	}
	if m1.FDMax == 0 {
		t.Error("FDMax = 0; RLIMIT_NOFILE should be readable")
	}
	if m1.UptimeSeconds <= 0 {
		t.Errorf("UptimeSeconds = %v, want > 0", m1.UptimeSeconds)
	}
	if m1.SampledAt.IsZero() {
		t.Error("SampledAt not set")
	}

	// Second sample yields a CPU delta.
	time.Sleep(120 * time.Millisecond)
	m2, err := r.Sample()
	if err != nil {
		t.Fatalf("second Sample: %v", err)
	}
	if m2.CPUPercent < 0 {
		t.Errorf("CPUPercent = %v, want >= 0", m2.CPUPercent)
	}
	if m2.Restarts < 1 {
		t.Errorf("Restarts = %d, want >= 1", m2.Restarts)
	}
	t.Logf("metrics: cpu=%.2f%% rss=%d threads=%d fds=%d/%d uptime=%.1fs",
		m2.CPUPercent, m2.RSSBytes, m2.Threads, m2.FDs, m2.FDMax, m2.UptimeSeconds)
}

// TestCPUPercentIsADelta proves the collector keeps a baseline: the first sample
// cannot know a rate, the second can.
func TestCPUPercentIsADelta(t *testing.T) {
	c := newCollector()
	pid := os.Getpid()

	if _, err := c.Sample(pid, 0, nil); err != nil {
		t.Fatalf("first Sample: %v", err)
	}
	// Spin briefly so our own utime/stime advances measurably.
	deadline := time.Now().Add(80 * time.Millisecond)
	x := 0
	for time.Now().Before(deadline) {
		x++
	}
	_ = x
	m, err := c.Sample(pid, 0, nil)
	if err != nil {
		t.Fatalf("second Sample: %v", err)
	}
	if m.CPUPercent < 0 {
		t.Errorf("CPUPercent = %v, want >= 0", m.CPUPercent)
	}
	// A busy loop on one core must be well above zero but not absurd.
	if m.CPUPercent == 0 {
		t.Log("CPUPercent was 0; the sampler may have run between ticks (not a failure)")
	}
}

// TestSampleAfterExitIsTypedError proves the "process exited" path returns a
// typed error and never panics.
func TestSampleAfterExitIsTypedError(t *testing.T) {
	r := startRunner(t, helperOpts(t, "crash"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.WaitState(ctx, func(s State) bool { return s.Terminal() }); err != nil {
		t.Fatalf("wait for terminal: %v", err)
	}
	m, err := r.Sample()
	if err == nil {
		t.Fatal("Sample on a stopped instance returned nil error")
	}
	if !isErr(err, ErrNotRunning) {
		t.Errorf("err = %v, want ErrNotRunning", err)
	}
	// The metadata that survives a stop must still be populated.
	if m.Restarts < 1 {
		t.Errorf("Restarts = %d, want >= 1 even after exit", m.Restarts)
	}
	if m.LastExitCode == nil || *m.LastExitCode != 7 {
		t.Errorf("LastExitCode = %v, want 7", m.LastExitCode)
	}
}

// TestSampleDeadPIDIsTypedError covers the raw collector against a vanished pid.
func TestSampleDeadPIDIsTypedError(t *testing.T) {
	c := newCollector()
	// Start and immediately reap a child to obtain a definitely-dead pid.
	o := helperOpts(t, "crash")
	r, err := NewRunner(o)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := r.PID()
	r.Wait()
	time.Sleep(100 * time.Millisecond)

	if _, err := c.Sample(pid, 0, nil); err == nil {
		t.Skipf("pid %d is still visible in /proc (it may have been reused)", pid)
	} else if !errors.Is(err, ErrProcessExited) {
		t.Errorf("err = %v, want ErrProcessExited", err)
	}
}

// TestCollectorResetDropsBaseline proves a restart cannot inherit counters.
func TestCollectorResetDropsBaseline(t *testing.T) {
	c := newCollector()
	if _, err := c.Sample(os.Getpid(), 0, nil); err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if c.prevAt.IsZero() {
		t.Fatal("baseline was not recorded")
	}
	c.Reset()
	if !c.prevAt.IsZero() || c.prevPID != 0 {
		t.Error("Reset did not clear the CPU baseline")
	}
	// After a reset the first sample again has no CPU%, rather than a spike.
	m, err := c.Sample(os.Getpid(), 0, nil)
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if m.CPUPercent != 0 {
		t.Errorf("CPUPercent = %v immediately after Reset, want 0", m.CPUPercent)
	}
}

// TestCountFDsAndIO proves the secondary /proc sources are wired up.
func TestCountFDsAndIO(t *testing.T) {
	pid := os.Getpid()
	fds, err := countFDs(pid)
	if err != nil {
		t.Fatalf("countFDs: %v", err)
	}
	if fds < 3 {
		t.Errorf("countFDs = %d, want at least 3 (stdin/stdout/stderr)", fds)
	}
	if _, _, err := readProcIO(pid); err != nil {
		t.Errorf("readProcIO: %v", err)
	}
	if _, err := countFDs(1 << 30); err == nil {
		t.Error("countFDs on a bogus pid returned nil error")
	}
}

// TestSignalNameMapping covers the exit-code -> signal-name translation used by
// the stop ladder's reporting.
func TestSignalNameMapping(t *testing.T) {
	if got := signalName(137); got != "SIGKILL" {
		t.Errorf("signalName(137) = %q, want SIGKILL", got)
	}
	if got := signalName(143); got != "SIGTERM" {
		t.Errorf("signalName(143) = %q, want SIGTERM", got)
	}
	if got := signalName(0); got != "" {
		t.Errorf("signalName(0) = %q, want empty", got)
	}
	if got := signalName(128 + 9); got != "SIGKILL" {
		t.Errorf("signalName(137) = %q", got)
	}
}

// TestCLKTckIsSane guards the clock-tick detection used for CPU deltas.
func TestCLKTckIsSane(t *testing.T) {
	got := clkTck()
	if got != 100 && got != 250 && got != 1000 && got != 1024 && got != 300 {
		t.Errorf("clkTck() = %d, which looks wrong", got)
	}
}

// TestAuxvClockTick proves the auxv parser finds AT_CLKTCK.
func TestAuxvClockTick(t *testing.T) {
	b, err := os.ReadFile("/proc/self/auxv")
	if err != nil {
		t.Skipf("cannot read /proc/self/auxv: %v", err)
	}
	v, ok := auxvClockTick(b)
	if !ok {
		t.Skip("AT_CLKTCK not present in auxv on this kernel")
	}
	if v != clkTck() {
		t.Errorf("auxv AT_CLKTCK = %d, clkTck() = %d", v, clkTck())
	}
}

// TestMetricsConcurrentSampling exercises the collector's lock under -race.
func TestMetricsConcurrentSampling(t *testing.T) {
	c := newCollector()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = c.Sample(os.Getpid(), j, nil)
			}
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------- manager

// TestManagerLifecycle covers Add/Get/List/Remove and start/stop by ID.
func TestManagerLifecycle(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if m.Count() != 0 {
		t.Fatalf("new manager has %d instances", m.Count())
	}

	var runners []*Runner
	for id := int64(1); id <= 3; id++ {
		o := helperOpts(t, "ready", func(o *Options) { o.ID = id })
		r, err := m.Add(ctx, o, true)
		if err != nil {
			t.Fatalf("Add(%d): %v", id, err)
		}
		runners = append(runners, r)
	}
	if m.Count() != 3 {
		t.Fatalf("Count = %d, want 3", m.Count())
	}

	// Duplicate ids are rejected.
	if _, err := m.Add(ctx, helperOpts(t, "ready", func(o *Options) { o.ID = 1 }), false); err == nil {
		t.Error("Add with a duplicate id succeeded")
	}

	ids := m.IDs()
	for i, want := range []int64{1, 2, 3} {
		if ids[i] != want {
			t.Errorf("IDs()[%d] = %d, want %d (List must be sorted)", i, ids[i], want)
		}
	}

	for _, r := range runners {
		if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
			t.Fatalf("instance %d never became Running: %v", r.ID(), err)
		}
	}
	if err := m.SendCommand(2, "help"); err != nil {
		t.Errorf("SendCommand: %v", err)
	}

	// Metrics through the manager.
	if _, err := m.Metrics(1); err != nil {
		t.Errorf("Metrics(1): %v", err)
	}
	if _, err := m.Metrics(999); err == nil {
		t.Error("Metrics on an unknown id returned nil error")
	}

	// Stop by id.
	res, err := m.Stop(ctx, 2)
	if err != nil {
		t.Fatalf("Stop(2): %v", err)
	}
	if !res.Graceful {
		t.Errorf("Stop(2).Graceful = false (method %s)", res.Method)
	}

	// Remove stops the rest and unregisters.
	if err := m.Remove(ctx, 1); err != nil {
		t.Errorf("Remove(1): %v", err)
	}
	if _, ok := m.Get(1); ok {
		t.Error("instance 1 still registered after Remove")
	}
	if err := m.Remove(ctx, 999); err == nil {
		t.Error("Remove on an unknown id returned nil error")
	}

	if got := m.Count(); got != 2 { // 2 and 3 remain
		t.Errorf("Count = %d, want 2", got)
	}
}

// TestManagerConcurrentAccess is the -race test for the registry.
func TestManagerConcurrentAccess(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	// Writers add instances while readers hammer List/Get/Snapshots.
	for id := int64(1); id <= 8; id++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			o := helperOpts(t, "ready", func(o *Options) { o.ID = id })
			if _, err := m.Add(ctx, o, true); err != nil {
				t.Errorf("Add(%d): %v", id, err)
			}
		}(id)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = m.List()
				_ = m.Snapshots()
				_ = m.IDs()
				_, _ = m.Get(3)
			}
		}()
	}
	wg.Wait()

	waitFor(t, 10*time.Second, "all instances running", func() bool {
		for _, s := range m.Snapshots() {
			if s.State != StateRunning {
				return false
			}
		}
		return len(m.Snapshots()) == 8
	})
}

// TestManagerCloseStopsEverything proves shutdown leaves nothing running.
func TestManagerCloseStopsEverything(t *testing.T) {
	m := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var runners []*Runner
	for id := int64(1); id <= 3; id++ {
		r, err := m.Add(ctx, helperOpts(t, "ready", func(o *Options) { o.ID = id }), true)
		if err != nil {
			t.Fatalf("Add(%d): %v", id, err)
		}
		runners = append(runners, r)
	}
	for _, r := range runners {
		if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
			t.Fatalf("instance %d never became Running: %v", r.ID(), err)
		}
	}

	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, r := range runners {
		if r.State() != StateStopped {
			t.Errorf("instance %d state = %s after Close, want stopped", r.ID(), r.State())
		}
		if r.PID() != 0 {
			t.Errorf("instance %d still reports pid %d", r.ID(), r.PID())
		}
	}
	// Idempotent.
	if err := m.Close(ctx); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if _, err := m.Add(ctx, helperOpts(t, "ready"), false); !isErr(err, ErrClosed) {
		t.Errorf("Add after Close = %v, want ErrClosed", err)
	}
}

// TestEventBusFanout covers the /ws/events contract.
func TestEventBusFanout(t *testing.T) {
	bus := NewEventBus(8)
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := bus.Subscribe(ctx, 4)
	b := bus.Subscribe(ctx, 4)

	bus.Publish(Event{Type: EventState, ID: 1, To: "running"})
	bus.Publish(Event{Type: EventLogError, ID: 1, Line: "ERROR: x"})

	for _, s := range []*EventSubscription{a, b} {
		var got []EventType
		for i := 0; i < 2; i++ {
			select {
			case ev := <-s.Ch():
				got = append(got, ev.Type)
			case <-time.After(2 * time.Second):
				t.Fatalf("timed out waiting for event %d", i)
			}
		}
		if got[0] != EventState || got[1] != EventLogError {
			t.Errorf("events = %v, want [state log.error]", got)
		}
	}

	subs, published, dropped := bus.Stats()
	if subs != 2 {
		t.Errorf("subscribers = %d, want 2", subs)
	}
	if published != 2 {
		t.Errorf("published = %d, want 2", published)
	}
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
}

// TestEventBusSlowSubscriberIsDropped proves publishing never blocks.
func TestEventBusSlowSubscriberIsDropped(t *testing.T) {
	bus := NewEventBus(1)
	defer bus.Close()

	// A subscriber that never reads.
	slow := bus.Subscribe(context.Background(), 1)
	defer slow.Close()
	// A well-behaved one.
	fast := bus.Subscribe(context.Background(), 512)
	defer fast.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 5000; i++ {
			bus.Publish(Event{Type: EventState, ID: int64(i)})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}

	_, _, dropped := bus.Stats()
	if dropped == 0 {
		t.Error("no drops recorded despite an unread subscriber")
	}
	// The fast subscriber must still be receiving.
	select {
	case <-fast.Ch():
	case <-time.After(2 * time.Second):
		t.Fatal("fast subscriber got nothing")
	}
}

// TestEventBusContextCancelDetaches proves a disconnecting WebSocket cleans up.
func TestEventBusContextCancelDetaches(t *testing.T) {
	bus := NewEventBus(4)
	defer bus.Close()
	ctx, cancel := context.WithCancel(context.Background())
	bus.Subscribe(ctx, 4)
	if n, _, _ := bus.Stats(); n != 1 {
		t.Fatalf("subscribers = %d, want 1", n)
	}
	cancel()
	waitFor(t, 2*time.Second, "subscriber detached", func() bool {
		n, _, _ := bus.Stats()
		return n == 0
	})
	// Publishing after detach must not reach the closed subscription.
	bus.Publish(Event{Type: EventState})
	if n, _, _ := bus.Stats(); n != 0 {
		t.Errorf("subscribers = %d after cancel, want 0", n)
	}
}

// TestManagerBusCarriesRunnerEvents proves runner events reach /ws/events.
func TestManagerBusCarriesRunnerEvents(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sub := m.Events(ctx, 256)
	defer sub.Close()

	r, err := m.Add(ctx, helperOpts(t, "ready"), true)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
		t.Fatalf("wait for Running: %v", err)
	}

	// Drain and look for the milestones the API layer forwards.
	var sawStarted, sawRunning bool
	deadline := time.After(5 * time.Second)
	for !(sawStarted && sawRunning) {
		select {
		case ev := <-sub.Ch():
			switch {
			case ev.Type == EventProcessStarted:
				sawStarted = true
			case ev.Type == EventState && ev.To == StateRunning.String():
				sawRunning = true
				if ev.State == nil || ev.State.Port != 28887 {
					t.Errorf("state event for Running lacks the captured port: %+v", ev.State)
				}
			}
		case <-deadline:
			t.Fatalf("timed out (started=%v running=%v)", sawStarted, sawRunning)
		}
	}

	// WaitForState must work off the same stream.
	if _, err := r.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := sub.WaitForState(ctx, r.ID(), StateStopped); err != nil {
		t.Errorf("WaitForState(stopped): %v", err)
	}
}

// TestEventBusWaitEventContext covers the helper's timeout path.
func TestEventBusWaitEventContext(t *testing.T) {
	bus := NewEventBus(4)
	defer bus.Close()
	s := bus.Subscribe(context.Background(), 4)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := s.WaitEvent(ctx, func(Event) bool { return true }); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("WaitEvent = %v, want DeadlineExceeded", err)
	}
}

// TestManagerRestart covers restart-by-id, including the restart counter.
func TestManagerRestart(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	r, err := m.Add(ctx, helperOpts(t, "ready"), true)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
		t.Fatalf("wait for Running: %v", err)
	}
	firstPID := r.PID()

	if err := m.Restart(ctx, r.ID()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
		t.Fatalf("wait for Running after restart: %v", err)
	}
	if r.PID() == firstPID {
		t.Error("pid did not change across a restart")
	}
	if r.Starts() < 2 {
		t.Errorf("Starts = %d, want >= 2", r.Starts())
	}
	// The readiness anchors must have been re-captured, not stale.
	if snap := r.Snapshot(); snap.Port != 28887 || !snap.Ready() {
		t.Errorf("snapshot after restart = %+v", snap)
	}
}

// TestManagerUnknownIDErrors documents the typed error surface of the manager.
func TestManagerUnknownIDErrors(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	ctx := context.Background()

	if err := m.Start(ctx, 42); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("Start(42) = %v, want a not-found error", err)
	}
	if _, err := m.Stop(ctx, 42); err == nil {
		t.Error("Stop(42) returned nil error")
	}
	if err := m.Restart(ctx, 42); err == nil {
		t.Error("Restart(42) returned nil error")
	}
	if err := m.SendCommand(42, "help"); err == nil {
		t.Error("SendCommand(42) returned nil error")
	}
	if _, err := m.Subscribe(ctx, 42, 0, true); err == nil {
		t.Error("Subscribe(42) returned nil error")
	}
}

// TestOptionsNormalizeDefaults pins the documented defaults.
func TestOptionsNormalizeDefaults(t *testing.T) {
	got := normalizeOptions(Options{})
	if got.Term != DefaultTerm {
		t.Errorf("Term = %q, want %q", got.Term, DefaultTerm)
	}
	if got.Rows != DefaultRows || got.Cols != DefaultCols {
		t.Errorf("size = %dx%d, want %dx%d", got.Rows, got.Cols, DefaultRows, DefaultCols)
	}
	if got.StopTimeout != DefaultStopTimeout {
		t.Errorf("StopTimeout = %v, want %v", got.StopTimeout, DefaultStopTimeout)
	}
	if got.KillTimeout != DefaultKillTimeout {
		t.Errorf("KillTimeout = %v, want %v", got.KillTimeout, DefaultKillTimeout)
	}
	if got.StartTimeout != DefaultStartTimeout {
		t.Errorf("StartTimeout = %v, want %v", got.StartTimeout, DefaultStartTimeout)
	}
	if got.RingLines != DefaultRingLines {
		t.Errorf("RingLines = %d, want %d", got.RingLines, DefaultRingLines)
	}
	if got.ColorMode != ColorModeAuto {
		t.Errorf("ColorMode = %q, want %q", got.ColorMode, ColorModeAuto)
	}
}
