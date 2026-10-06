package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeNotifier is a scriptable Notifier for dispatcher tests.
type fakeNotifier struct {
	name string

	mu       sync.Mutex
	sent     []Notification
	attempts int

	// failFirst makes the first n deliveries fail.
	failFirst int
	// alwaysFail makes every delivery fail.
	alwaysFail bool
	// blockUntil, when non-nil, blocks each delivery until it is closed.
	blockUntil chan struct{}
	// blockFor blocks each delivery for this long (ignoring the context, to
	// simulate a notifier that does not honour cancellation).
	blockFor time.Duration
	// panicOnSend panics inside Send.
	panicOnSend bool

	// calls counts invocations.
	calls atomic.Int64
}

func newFakeNotifier(name string) *fakeNotifier {
	return &fakeNotifier{name: name}
}

func (f *fakeNotifier) Name() string { return f.name }

func (f *fakeNotifier) Send(ctx context.Context, n Notification) error {
	f.calls.Add(1)

	f.mu.Lock()
	f.sent = append(f.sent, n)
	f.attempts++
	attempt := f.attempts
	blockUntil := f.blockUntil
	blockFor := f.blockFor
	panicOnSend := f.panicOnSend
	alwaysFail := f.alwaysFail
	failFirst := f.failFirst
	f.mu.Unlock()

	if panicOnSend {
		panic("fake notifier exploded")
	}

	if blockUntil != nil {
		select {
		case <-blockUntil:
		case <-ctx.Done():
			// A well-behaved notifier returns on cancellation.
			return ctx.Err()
		}
	}
	if blockFor > 0 {
		// Deliberately ignores ctx: this simulates the hostile case the
		// dispatcher must survive.
		time.Sleep(blockFor)
	}

	if alwaysFail {
		return fmt.Errorf("%s: always fails", f.name)
	}
	if attempt <= failFirst {
		return fmt.Errorf("%s: transient failure %d", f.name, attempt)
	}
	return nil
}

// sentCount reports how many notifications were recorded.
func (f *fakeNotifier) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// testLogger discards output so a deliberately-failing test does not spam.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestDispatcher builds a started dispatcher with fast timings and an
// instant backoff sleeper.
func newTestDispatcher(t *testing.T, notifiers []Notifier, opts Options) *Dispatcher {
	t.Helper()

	if opts.Logger == nil {
		opts.Logger = testLogger()
	}
	if opts.BaseBackoff == 0 {
		opts.BaseBackoff = time.Millisecond
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = 2 * time.Millisecond
	}
	if opts.Timeout == 0 {
		opts.Timeout = 2 * time.Second
	}
	if opts.DedupWindow == 0 {
		opts.DedupWindow = 50 * time.Millisecond
	}

	d := NewDispatcher(notifiers, opts)

	// Skip real backoff waits: the retry *count* is what these tests assert.
	d.SetSleep(func(ctx context.Context, _ time.Duration) bool {
		return ctx.Err() == nil
	})

	d.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})
	return d
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

// TestDispatcherDeliversToAllNotifiers is the baseline: one notification, three
// channels, all three receive it.
func TestDispatcherDeliversToAllNotifiers(t *testing.T) {
	t.Parallel()

	a, b, c := newFakeNotifier("a"), newFakeNotifier("b"), newFakeNotifier("c")
	d := newTestDispatcher(t, []Notifier{a, b, c}, Options{})

	if err := d.Send(sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	waitFor(t, 3*time.Second, "all three notifiers to receive the notification", func() bool {
		return a.sentCount() == 1 && b.sentCount() == 1 && c.sentCount() == 1
	})

	results := d.Results()
	if len(results) != 3 {
		t.Fatalf("Results() = %d entries, want 3", len(results))
	}
	for _, r := range results {
		if !r.OK {
			t.Errorf("result for %s = %+v, want OK", r.Notifier, r)
		}
		if r.Attempts != 1 {
			t.Errorf("result for %s took %d attempts, want 1", r.Notifier, r.Attempts)
		}
	}

	stats := d.Stats()
	if stats.Enqueued != 1 || stats.Delivered != 3 || stats.Failed != 0 {
		t.Errorf("Stats() = %+v, want enqueued 1 / delivered 3 / failed 0", stats)
	}
}

// TestDispatcherSendDoesNotBlock is the core contract: Send returns
// immediately even when a channel hangs.
func TestDispatcherSendDoesNotBlock(t *testing.T) {
	t.Parallel()

	blocker := newFakeNotifier("blocker")
	blocker.blockUntil = make(chan struct{}) // never closed
	defer close(blocker.blockUntil)

	fast := newFakeNotifier("fast")
	d := newTestDispatcher(t, []Notifier{blocker, fast}, Options{Timeout: 5 * time.Second})

	start := time.Now()
	if err := d.Send(sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("Send blocked for %v; it must return as soon as the notification is queued", elapsed)
	}

	// The fast channel must not be held up by the hanging one.
	waitFor(t, 2*time.Second, "the fast notifier to be served despite the hanging one", func() bool {
		return fast.sentCount() == 1
	})
}

// TestDispatcherHangingNotifierDoesNotBlockOthers is the §6.7 requirement made
// explicit: a hanging channel must not delay or break the others, and its
// delivery must be recorded as a timeout failure rather than hanging forever.
func TestDispatcherHangingNotifierDoesNotBlockOthers(t *testing.T) {
	t.Parallel()

	// This notifier ignores context cancellation, so the dispatcher's own
	// timeout must be what ends the wait.
	hang := newFakeNotifier("hang")
	hang.blockFor = 10 * time.Second

	ok1, ok2 := newFakeNotifier("ok1"), newFakeNotifier("ok2")
	d := newTestDispatcher(t, []Notifier{hang, ok1, ok2}, Options{
		Timeout:     150 * time.Millisecond,
		MaxAttempts: 1,
	})

	start := time.Now()
	if err := d.Send(sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	waitFor(t, 3*time.Second, "both healthy notifiers to succeed", func() bool {
		return ok1.sentCount() == 1 && ok2.sentCount() == 1
	})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("healthy notifiers were delayed %v by the hanging one", elapsed)
	}

	// The hanging channel must be reported as failed, not as delivered.
	waitFor(t, 3*time.Second, "the hanging notifier's result to be recorded", func() bool {
		for _, r := range d.Results() {
			if r.Notifier == "hang" {
				return true
			}
		}
		return false
	})

	for _, r := range d.Results() {
		switch r.Notifier {
		case "ok1", "ok2":
			if !r.OK {
				t.Errorf("%s failed: %+v", r.Notifier, r)
			}
		case "hang":
			if r.OK {
				t.Error("the hanging notifier must not be reported as delivered")
			}
			if !strings.Contains(r.Err, "context deadline exceeded") &&
				!strings.Contains(r.Err, "deadline") {
				t.Errorf("hang result error = %q, want a timeout", r.Err)
			}
		}
	}

	stats := d.Stats()
	if stats.Delivered != 2 {
		t.Errorf("Delivered = %d, want 2 (the hanging channel excluded)", stats.Delivered)
	}
	if stats.Failed != 1 {
		t.Errorf("Failed = %d, want 1", stats.Failed)
	}
}

// TestDispatcherPanickingNotifierIsContained proves a panicking channel cannot
// crash the dispatcher or the other channels.
func TestDispatcherPanickingNotifierIsContained(t *testing.T) {
	t.Parallel()

	boom := newFakeNotifier("boom")
	boom.panicOnSend = true

	healthy := newFakeNotifier("healthy")
	d := newTestDispatcher(t, []Notifier{boom, healthy}, Options{MaxAttempts: 1})

	if err := d.Send(sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	waitFor(t, 3*time.Second, "the healthy notifier to succeed despite the panic", func() bool {
		return healthy.sentCount() == 1
	})

	var boomResult *Result
	for _, r := range d.Results() {
		if r.Notifier == "boom" {
			cp := r
			boomResult = &cp
		}
	}
	if boomResult == nil {
		t.Fatal("the panicking notifier produced no result")
	}
	if boomResult.OK {
		t.Error("a panicking notifier must not be reported as delivered")
	}
	if !strings.Contains(boomResult.Err, "panicked") {
		t.Errorf("result error = %q, want it to mention the panic", boomResult.Err)
	}

	// The dispatcher must still be usable afterwards.
	healthy2 := newFakeNotifier("healthy2")
	_ = healthy2
	if err := d.Send(Notification{Event: EventServerReady, Title: "still alive"}); err != nil {
		t.Fatalf("Send after a panic: %v (the dispatcher must survive)", err)
	}
	waitFor(t, 3*time.Second, "the healthy notifier to receive the second notification", func() bool {
		return healthy.sentCount() == 2
	})
}

// TestDispatcherRetriesTransientFailures checks that a channel failing twice
// and then succeeding is retried and ultimately reported as delivered.
func TestDispatcherRetriesTransientFailures(t *testing.T) {
	t.Parallel()

	flaky := newFakeNotifier("flaky")
	flaky.failFirst = 2

	d := newTestDispatcher(t, []Notifier{flaky}, Options{MaxAttempts: 3})

	if err := d.Send(sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	waitFor(t, 3*time.Second, "the flaky notifier to succeed on its third attempt", func() bool {
		for _, r := range d.Results() {
			if r.Notifier == "flaky" {
				return r.OK
			}
		}
		return false
	})

	for _, r := range d.Results() {
		if r.Notifier != "flaky" {
			continue
		}
		if r.Attempts != 3 {
			t.Errorf("Attempts = %d, want 3", r.Attempts)
		}
		if r.Err != "" {
			t.Errorf("Err = %q, want empty after eventual success", r.Err)
		}
	}
	if got := flaky.calls.Load(); got != 3 {
		t.Errorf("Send was called %d times, want 3", got)
	}
}

// TestDispatcherGivesUpAfterMaxAttempts checks the retry budget is honoured.
func TestDispatcherGivesUpAfterMaxAttempts(t *testing.T) {
	t.Parallel()

	broken := newFakeNotifier("broken")
	broken.alwaysFail = true

	d := newTestDispatcher(t, []Notifier{broken}, Options{MaxAttempts: 3})

	if err := d.Send(sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	waitFor(t, 3*time.Second, "the failing notifier to exhaust its attempts", func() bool {
		return broken.calls.Load() == 3
	})

	// Give the result recording a moment, then confirm no fourth call happens.
	time.Sleep(50 * time.Millisecond)
	if got := broken.calls.Load(); got != 3 {
		t.Errorf("Send was called %d times, want exactly 3 (MaxAttempts)", got)
	}

	for _, r := range d.Results() {
		if r.OK {
			t.Error("a permanently failing notifier must not be reported as delivered")
		}
		if r.Attempts != 3 {
			t.Errorf("Attempts = %d, want 3", r.Attempts)
		}
		if !strings.Contains(r.Err, "always fails") {
			t.Errorf("Err = %q, want the underlying failure", r.Err)
		}
	}
	if stats := d.Stats(); stats.Failed != 1 {
		t.Errorf("Failed = %d, want 1", stats.Failed)
	}
}

// TestDispatcherBackoffGrowsAndIsJittered checks the backoff computation
// directly: exponential growth, capped, with jitter applied.
func TestDispatcherBackoffGrowsAndIsJittered(t *testing.T) {
	t.Parallel()

	d := NewDispatcher(nil, Options{
		BaseBackoff: 100 * time.Millisecond,
		MaxBackoff:  time.Second,
		Logger:      testLogger(),
	})

	// base<<0 = 100ms, plus up to 50ms of jitter.
	first := d.backoff(1)
	if first < 100*time.Millisecond || first > 150*time.Millisecond {
		t.Errorf("backoff(1) = %v, want in [100ms, 150ms]", first)
	}

	// base<<1 = 200ms, plus up to 100ms.
	second := d.backoff(2)
	if second < 200*time.Millisecond || second > 300*time.Millisecond {
		t.Errorf("backoff(2) = %v, want in [200ms, 300ms]", second)
	}

	// base<<4 = 1600ms, capped at 1s, plus up to 500ms.
	fifth := d.backoff(5)
	if fifth < time.Second || fifth > 1500*time.Millisecond {
		t.Errorf("backoff(5) = %v, want the cap (1s) plus jitter", fifth)
	}

	// A huge attempt number must not overflow the shift.
	huge := d.backoff(64)
	if huge < time.Second || huge > 1500*time.Millisecond {
		t.Errorf("backoff(64) = %v, want the capped value (no overflow)", huge)
	}

	// Jitter must actually vary the value.
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		seen[d.backoff(1)] = true
	}
	if len(seen) < 2 {
		t.Error("backoff produced no jitter; every retry would be in lockstep")
	}
}

// TestDispatcherDedupSuppressesRepeats is §6.7's anti-spam requirement.
func TestDispatcherDedupSuppressesRepeats(t *testing.T) {
	t.Parallel()

	fake := newFakeNotifier("ch")
	d := newTestDispatcher(t, []Notifier{fake}, Options{DedupWindow: time.Hour})

	n := Notification{
		Event: EventInstanceCrashed, Instance: "inst-1", Title: "crashed",
	}

	// Ten identical events: only the first may be delivered.
	for i := 0; i < 10; i++ {
		if err := d.Send(n); err != nil {
			t.Fatalf("Send #%d: %v", i, err)
		}
	}

	waitFor(t, 3*time.Second, "the first notification to be delivered", func() bool {
		return fake.sentCount() == 1
	})
	time.Sleep(100 * time.Millisecond)

	if got := fake.sentCount(); got != 1 {
		t.Errorf("delivered %d notifications, want 1 (dedup window active)", got)
	}
	if stats := d.Stats(); stats.Suppressed != 9 {
		t.Errorf("Suppressed = %d, want 9", stats.Suppressed)
	}
}

// TestDispatcherDedupIsPerInstance checks the key granularity: the same event
// on two instances is two distinct events and both go out.
func TestDispatcherDedupIsPerInstance(t *testing.T) {
	t.Parallel()

	fake := newFakeNotifier("ch")
	d := newTestDispatcher(t, []Notifier{fake}, Options{DedupWindow: time.Hour})

	for _, inst := range []string{"a", "b", "a", "b"} {
		if err := d.Send(Notification{
			Event: EventInstanceStarted, Instance: inst, Title: "started",
		}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	waitFor(t, 3*time.Second, "both instances' notifications to arrive", func() bool {
		return fake.sentCount() == 2
	})
	time.Sleep(100 * time.Millisecond)

	if got := fake.sentCount(); got != 2 {
		t.Errorf("delivered %d notifications, want 2 (one per instance)", got)
	}
}

// TestDispatcherDedupIsPerEvent checks that different events on one instance
// are not collapsed together.
func TestDispatcherDedupIsPerEvent(t *testing.T) {
	t.Parallel()

	fake := newFakeNotifier("ch")
	d := newTestDispatcher(t, []Notifier{fake}, Options{DedupWindow: time.Hour})

	for _, ev := range []string{EventInstanceStarted, EventServerReady, EventBackupCompleted} {
		if err := d.Send(Notification{Event: ev, Instance: "x", Title: ev}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	waitFor(t, 3*time.Second, "all three events to arrive", func() bool {
		return fake.sentCount() == 3
	})
	if got := fake.sentCount(); got != 3 {
		t.Errorf("delivered %d notifications, want 3", got)
	}
}

// TestDispatcherDedupWindowExpires checks that the suppression is a window, not
// a permanent block: after it lapses the same event must be delivered again.
func TestDispatcherDedupWindowExpires(t *testing.T) {
	t.Parallel()

	fake := newFakeNotifier("ch")
	d := newTestDispatcher(t, []Notifier{fake}, Options{DedupWindow: 40 * time.Millisecond})

	n := Notification{Event: EventDiskLow, Instance: "host", Title: "disk low"}

	if err := d.Send(n); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFor(t, 2*time.Second, "the first notification", func() bool { return fake.sentCount() == 1 })

	// Still inside the window.
	if err := d.Send(n); err != nil {
		t.Fatalf("Send: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if got := fake.sentCount(); got != 1 {
		t.Fatalf("delivered %d notifications inside the window, want 1", got)
	}

	// Past the window.
	time.Sleep(60 * time.Millisecond)
	if err := d.Send(n); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFor(t, 2*time.Second, "the notification after the window lapsed", func() bool {
		return fake.sentCount() == 2
	})
}

// TestDispatcherDedupDisabled checks that a zero window means "send everything",
// which is what an operator wants for a low-volume channel.
func TestDispatcherDedupDisabled(t *testing.T) {
	t.Parallel()

	fake := newFakeNotifier("ch")

	opts := Options{
		DedupWindow: 0,
		Logger:      testLogger(),
		Timeout:     2 * time.Second,
		BaseBackoff: time.Millisecond,
		MaxBackoff:  2 * time.Millisecond,
		MaxAttempts: 1,
	}
	d := NewDispatcher([]Notifier{fake}, opts)
	d.SetSleep(func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil })
	d.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})

	n := Notification{Event: EventBackupCompleted, Instance: "x", Title: "ok"}
	for i := 0; i < 3; i++ {
		if err := d.Send(n); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	waitFor(t, 3*time.Second, "all three notifications", func() bool { return fake.sentCount() == 3 })
	if stats := d.Stats(); stats.Suppressed != 0 {
		t.Errorf("Suppressed = %d, want 0 when dedup is disabled", stats.Suppressed)
	}
}

// TestDispatcherEventFilter checks per-notifier event routing.
func TestDispatcherEventFilter(t *testing.T) {
	t.Parallel()

	all := newFakeNotifier("all")
	errorsOnly := newFakeNotifier("errors-only")
	nothing := newFakeNotifier("nothing")

	d := newTestDispatcher(t, []Notifier{all, errorsOnly, nothing}, Options{
		DedupWindow: time.Hour,
		EventFilter: map[string][]string{
			"errors-only": {EventInstanceCrashed, EventBackupFailed},
			"nothing":     {},
		},
	})

	if err := d.Send(Notification{Event: EventInstanceStarted, Instance: "x"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFor(t, 3*time.Second, "the catch-all channel", func() bool { return all.sentCount() == 1 })

	if err := d.Send(Notification{Event: EventInstanceCrashed, Instance: "x"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFor(t, 3*time.Second, "the filtered channel to receive the crash", func() bool {
		return errorsOnly.sentCount() == 1
	})
	time.Sleep(50 * time.Millisecond)

	if got := all.sentCount(); got != 2 {
		t.Errorf("unfiltered channel received %d, want 2", got)
	}
	if got := errorsOnly.sentCount(); got != 1 {
		t.Errorf("filtered channel received %d, want 1 (only the crash)", got)
	}
	if got := nothing.sentCount(); got != 0 {
		t.Errorf("channel with an empty filter received %d, want 0", got)
	}

	// A skipped delivery is neither a success nor a failure.
	for _, r := range d.Results() {
		if r.Notifier == "nothing" && !r.Skipped {
			t.Errorf("result for the filtered-out channel = %+v, want Skipped", r)
		}
	}
	if stats := d.Stats(); stats.Failed != 0 {
		t.Errorf("Failed = %d, want 0 (a filtered event is not a failure)", stats.Failed)
	}
}

// TestDispatcherBoundedQueueDropsInsteadOfBlocking checks that a saturated
// queue reports drops rather than blocking the caller or growing without bound.
func TestDispatcherBoundedQueueDrops(t *testing.T) {
	t.Parallel()

	// One worker, permanently blocked, so the queue fills.
	blocker := newFakeNotifier("blocker")
	blocker.blockUntil = make(chan struct{})
	defer close(blocker.blockUntil)

	d := newTestDispatcher(t, []Notifier{blocker}, Options{
		Workers:     1,
		QueueSize:   2,
		Timeout:     time.Hour,
		MaxAttempts: 1,
	})

	var drops int
	for i := 0; i < 20; i++ {
		err := d.Send(Notification{Event: EventServerReady, Instance: fmt.Sprintf("i%d", i)})
		if errors.Is(err, ErrQueueFull) {
			drops++
		}
	}
	if drops == 0 {
		t.Fatal("a saturated queue must report drops with ErrQueueFull")
	}
	if got := d.Stats().Dropped; int(got) != drops {
		t.Errorf("Stats().Dropped = %d, want %d", got, drops)
	}
}

// TestDispatcherQueueDrainsOnStop checks that a graceful stop delivers what is
// already queued: losing the final "instance stopped" notice would be the
// worst possible time to drop a message.
func TestDispatcherQueueDrainsOnStop(t *testing.T) {
	t.Parallel()

	fake := newFakeNotifier("ch")

	d := NewDispatcher([]Notifier{fake}, Options{
		Workers:     1,
		QueueSize:   16,
		Timeout:     2 * time.Second,
		MaxAttempts: 1,
		BaseBackoff: time.Millisecond,
		MaxBackoff:  time.Millisecond,
		DedupWindow: time.Hour,
		Logger:      testLogger(),
	})
	d.SetSleep(func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil })
	d.Start()

	for i := 0; i < 8; i++ {
		if err := d.Send(Notification{
			Event: EventBackupCompleted, Instance: fmt.Sprintf("i%d", i), Title: "done",
		}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := fake.sentCount(); got != 8 {
		t.Errorf("delivered %d queued notifications before stopping, want 8", got)
	}
}

// TestDispatcherStopIsGracefulAndIdempotent checks Stop returns promptly,
// waits for in-flight work, and can be called more than once.
func TestDispatcherStopIsGracefulAndIdempotent(t *testing.T) {
	t.Parallel()

	slow := newFakeNotifier("slow")
	slow.blockFor = 100 * time.Millisecond

	d := NewDispatcher([]Notifier{slow}, Options{
		Timeout:     2 * time.Second,
		MaxAttempts: 1,
		BaseBackoff: time.Millisecond,
		MaxBackoff:  time.Millisecond,
		Logger:      testLogger(),
	})
	d.Start()

	if err := d.Send(sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Give the worker a moment to pick the job up.
	waitFor(t, 2*time.Second, "the delivery to start", func() bool { return slow.calls.Load() == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := d.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := slow.sentCount(); got != 1 {
		t.Errorf("in-flight delivery was not awaited: sent %d, want 1", got)
	}

	// Second call is a no-op, and Send now reports the dispatcher is stopped.
	if err := d.Stop(ctx); err != nil {
		t.Errorf("second Stop: %v, want nil", err)
	}
	if err := d.Send(sampleNotification()); !errors.Is(err, ErrStopped) {
		t.Errorf("Send after Stop = %v, want ErrStopped", err)
	}
}

// TestDispatcherStopHonoursContext checks Stop does not hang forever waiting on
// a wedged channel.
func TestDispatcherStopHonoursContext(t *testing.T) {
	t.Parallel()

	wedged := newFakeNotifier("wedged")
	wedged.blockFor = 3 * time.Second

	d := NewDispatcher([]Notifier{wedged}, Options{
		Timeout:     5 * time.Second,
		MaxAttempts: 1,
		BaseBackoff: time.Millisecond,
		MaxBackoff:  time.Millisecond,
		Logger:      testLogger(),
	})
	d.Start()

	if err := d.Send(sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFor(t, 2*time.Second, "the delivery to start", func() bool { return wedged.calls.Load() == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := d.Stop(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop = %v, want context.DeadlineExceeded", err)
	}
}

func TestDispatcherSendBeforeStart(t *testing.T) {
	t.Parallel()

	d := NewDispatcher(nil, Options{Logger: testLogger()})
	if err := d.Send(sampleNotification()); !errors.Is(err, ErrNotStarted) {
		t.Errorf("Send before Start = %v, want ErrNotStarted", err)
	}
	if _, err := d.SendWait(context.Background(), sampleNotification()); !errors.Is(err, ErrNotStarted) {
		t.Errorf("SendWait before Start = %v, want ErrNotStarted", err)
	}
}

// TestDispatcherNoNotifiersIsHarmless checks the "nothing configured yet" case,
// which is the panel's default state on first run.
func TestDispatcherNoNotifiersIsHarmless(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t, nil, Options{})

	if err := d.Send(sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	if got := d.Results(); len(got) != 0 {
		t.Errorf("Results() = %v, want empty", got)
	}
	if got := d.Notifiers(); len(got) != 0 {
		t.Errorf("Notifiers() = %v, want empty", got)
	}
}

// TestDispatcherIgnoresNilNotifiers checks that an unconfigured channel slot
// does not panic at delivery time.
func TestDispatcherIgnoresNilNotifiers(t *testing.T) {
	t.Parallel()

	fake := newFakeNotifier("real")
	d := newTestDispatcher(t, []Notifier{nil, fake, nil}, Options{})

	if err := d.Send(sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFor(t, 3*time.Second, "the real notifier", func() bool { return fake.sentCount() == 1 })

	if got := d.Notifiers(); len(got) != 1 || got[0] != "real" {
		t.Errorf("Notifiers() = %v, want [real]", got)
	}
}

func TestDispatcherStartIsIdempotent(t *testing.T) {
	t.Parallel()

	fake := newFakeNotifier("ch")
	d := newTestDispatcher(t, []Notifier{fake}, Options{})

	d.Start()
	d.Start()
	d.Start()

	if err := d.Send(sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFor(t, 3*time.Second, "exactly one delivery despite repeated Start", func() bool {
		return fake.sentCount() == 1
	})
	time.Sleep(50 * time.Millisecond)
	if got := fake.sentCount(); got != 1 {
		t.Errorf("delivered %d times, want 1 (Start must not multiply workers per job)", got)
	}
}

// TestDispatcherSendWait checks the synchronous path used by a "send test
// notification" UI action.
func TestDispatcherSendWait(t *testing.T) {
	t.Parallel()

	a, b := newFakeNotifier("a"), newFakeNotifier("b")
	d := newTestDispatcher(t, []Notifier{a, b}, Options{})

	results, err := d.SendWait(context.Background(), sampleNotification())
	if err != nil {
		t.Fatalf("SendWait: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if !r.OK {
			t.Errorf("result %+v, want OK", r)
		}
	}
}

func TestDispatcherSendWaitReportsFailures(t *testing.T) {
	t.Parallel()

	bad := newFakeNotifier("bad")
	bad.alwaysFail = true

	d := newTestDispatcher(t, []Notifier{bad}, Options{MaxAttempts: 2})

	results, err := d.SendWait(context.Background(), sampleNotification())
	if err != nil {
		t.Fatalf("SendWait must return results plus a nil error; the per-channel outcome is in the results: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].OK {
		t.Error("a failing channel must not be reported as OK")
	}
	if results[0].Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", results[0].Attempts)
	}
}

func TestDispatcherSendWaitHonoursContext(t *testing.T) {
	t.Parallel()

	hang := newFakeNotifier("hang")
	hang.blockFor = 2 * time.Second

	d := newTestDispatcher(t, []Notifier{hang}, Options{
		Timeout:     3 * time.Second,
		MaxAttempts: 1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := d.SendWait(ctx, sampleNotification()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendWait = %v, want context.DeadlineExceeded", err)
	}
}

// TestDispatcherConcurrentSends exercises the whole thing under -race with
// many concurrent producers and several channels, some failing.
func TestDispatcherConcurrentSends(t *testing.T) {
	t.Parallel()

	good := newFakeNotifier("good")
	flaky := newFakeNotifier("flaky")
	flaky.failFirst = 1
	bad := newFakeNotifier("bad")
	bad.alwaysFail = true

	d := newTestDispatcher(t, []Notifier{good, flaky, bad}, Options{
		Workers:     8,
		QueueSize:   256,
		MaxAttempts: 2,
		DedupWindow: time.Nanosecond, // effectively off, so every event is distinct
	})

	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				_ = d.Send(Notification{
					Event:    EventBackupCompleted,
					Instance: fmt.Sprintf("w%d-i%d", w, i),
					Title:    "backup done",
				})
			}
		}(w)
	}
	wg.Wait()

	// Note the units, because getting them wrong is the easy mistake: Enqueued
	// and Dropped count *notifications*, while Delivered and Failed count
	// *per-notifier deliveries*. With three channels the delivered+failed total
	// therefore converges on Enqueued*3, not on Enqueued.
	waitFor(t, 15*time.Second, "every accepted notification to reach every channel", func() bool {
		s := d.Stats()
		perChannel := s.Delivered + s.Failed
		return perChannel == int64(3)*s.Enqueued && s.Enqueued+s.Dropped == 400
	})

	s := d.Stats()
	if s.Enqueued+s.Dropped != 400 {
		t.Errorf("Enqueued+Dropped = %d, want 400 (every Send is either enqueued or dropped)",
			s.Enqueued+s.Dropped)
	}
	if s.Delivered == 0 {
		t.Error("nothing was delivered")
	}
	if s.Failed == 0 {
		t.Error("the permanently failing channel should have produced failures")
	}
	if got, want := good.sentCount(), int(s.Enqueued); got != want {
		t.Errorf("the healthy channel received %d, want %d (one per enqueued notification)",
			got, want)
	}
	if got, want := bad.sentCount(), 2*int(s.Enqueued); got != want {
		t.Errorf("the failing channel was invoked %d times, want %d (two attempts each)",
			got, want)
	}
}

// TestDispatcherWithRealHTTPChannelsEndToEnd runs the dispatcher against actual
// httptest servers through the real channel implementations, which is the
// closest thing to production this package can test without a vendor account.
func TestDispatcherWithRealHTTPChannelsEndToEnd(t *testing.T) {
	t.Parallel()

	handler := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}
	}

	okSrv := httptest.NewServer(handler(http.StatusOK, `{"errcode":0,"errmsg":"ok"}`))
	t.Cleanup(okSrv.Close)

	badSrv := httptest.NewServer(handler(http.StatusInternalServerError, "boom"))
	t.Cleanup(badSrv.Close)

	d := newTestDispatcher(t, []Notifier{
		NewDingTalk(DingTalkConfig{URL: okSrv.URL}),
		NewDiscord(DiscordConfig{URL: okSrv.URL}),
		NewWebhook(WebhookConfig{URL: badSrv.URL, Name: "broken-hook"}),
	}, Options{MaxAttempts: 1})

	results, err := d.SendWait(context.Background(), sampleNotification())
	if err != nil {
		t.Fatalf("SendWait: %v", err)
	}

	byName := map[string]Result{}
	for _, r := range results {
		byName[r.Notifier] = r
	}
	if !byName["dingtalk"].OK {
		t.Errorf("dingtalk = %+v, want OK", byName["dingtalk"])
	}
	if !byName["discord"].OK {
		t.Errorf("discord = %+v, want OK", byName["discord"])
	}
	if byName["broken-hook"].OK {
		t.Error("the 500-answering webhook must be reported as failed")
	}
	if !strings.Contains(byName["broken-hook"].Err, "500") {
		t.Errorf("broken-hook error = %q, want it to name the status", byName["broken-hook"].Err)
	}
}

// TestDispatcherResultsAreJSONSerialisable guards the API layer's use of Result.
func TestDispatcherResultsAreJSONSerialisable(t *testing.T) {
	t.Parallel()

	r := Result{
		Notifier: "ch", Event: EventInstanceCrashed, OK: false,
		Attempts: 3, Err: "boom", Duration: 1500 * time.Millisecond,
		At: time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC),
	}
	if r.Duration <= 0 {
		t.Fatal("sanity: duration should be positive")
	}
	if _, err := jsonMarshal(r); err != nil {
		t.Fatalf("Result is not JSON serialisable: %v", err)
	}
}

// jsonMarshal is a tiny indirection so the test file does not need to import
// encoding/json just for one assertion.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
