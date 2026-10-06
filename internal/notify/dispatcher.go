package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

// Dispatcher fans Notifications out to a set of Notifiers.
//
// The design constraints, and how each is met:
//
//   - "The caller never blocks." Send pushes onto a bounded channel and
//     returns. When the queue is full Send drops the notification and reports
//     why, rather than waiting — an unbounded queue would be a memory leak
//     waiting for a slow endpoint, and blocking would stall whatever produced
//     the event.
//   - "One channel cannot affect another." Each delivery runs in its own
//     goroutine under a per-notifier concurrency limiter, with a per-attempt
//     timeout and a panic guard. A hanging or panicking notifier is recorded
//     as a failure and nothing else notices.
//   - "Retry transient failures." Failed attempts are retried with exponential
//     backoff plus jitter, bounded by MaxAttempts and the dispatcher's
//     lifecycle context.
//
// The zero value is not usable; build one with NewDispatcher and call Start.
type Dispatcher struct {
	notifiers []Notifier

	queue       chan job
	workers     int
	timeout     time.Duration // per delivery attempt
	maxAttempts int
	baseBackoff time.Duration
	maxBackoff  time.Duration
	dedupWindow time.Duration

	// eventFilters maps a notifier name to the events it accepts. See
	// Options.EventFilter.
	eventFilters map[string]map[string]bool

	logger *slog.Logger

	// now and sleep are injectable so tests do not have to wait out backoff.
	now   func() time.Time
	sleep func(context.Context, time.Duration) bool

	// rand is guarded by randMu because math/rand's global source is
	// goroutine-safe but a *rand.Rand is not.
	randMu sync.Mutex
	rand   *rand.Rand

	mu      sync.Mutex
	seen    map[string]time.Time // dedup: key -> last accepted
	running bool
	started bool
	stop    chan struct{}
	wg      sync.WaitGroup

	// results records the most recent delivery per notifier, for the UI.
	lastMu  sync.Mutex
	results map[string]Result

	statsMu sync.Mutex
	stats   Stats
}

// job is one queued fan-out.
type job struct {
	notification Notification
}

// Result is the outcome of delivering one notification to one notifier.
type Result struct {
	// Notifier is the channel name.
	Notifier string `json:"notifier"`
	// Event is the notification's event.
	Event string `json:"event"`
	// OK reports whether delivery ultimately succeeded.
	OK bool `json:"ok"`
	// Attempts is how many attempts were made.
	Attempts int `json:"attempts"`
	// Err is the final error, empty on success.
	Err string `json:"err,omitempty"`
	// Skipped reports that the channel declined the notification (an event
	// filter), which is neither success nor failure.
	Skipped bool `json:"skipped,omitempty"`
	// Duration is how long the successful (or final) attempt took.
	Duration time.Duration `json:"duration"`
	// At is when the delivery finished.
	At time.Time `json:"at"`
}

// Stats is a counter snapshot for the settings/diagnostics view.
type Stats struct {
	// Enqueued counts notifications accepted by Send.
	Enqueued int64 `json:"enqueued"`
	// Dropped counts notifications rejected because the queue was full.
	Dropped int64 `json:"dropped"`
	// Suppressed counts notifications suppressed by the dedup window.
	Suppressed int64 `json:"suppressed"`
	// Delivered counts successful per-notifier deliveries.
	Delivered int64 `json:"delivered"`
	// Failed counts per-notifier deliveries that exhausted their attempts.
	Failed int64 `json:"failed"`
}

// Options configures a Dispatcher. Every zero field is replaced by a sane
// default, so Options{} is a valid configuration.
type Options struct {
	// Workers is how many notifications may be in flight concurrently.
	// Default 4.
	Workers int

	// QueueSize is the bounded queue depth. Default 128.
	QueueSize int

	// Timeout bounds one delivery attempt to one notifier. Default 10s.
	Timeout time.Duration

	// MaxAttempts is the total number of attempts per notifier, including the
	// first. Default 3 (one try plus two retries).
	MaxAttempts int

	// BaseBackoff is the delay before the second attempt; it doubles each
	// time, up to MaxBackoff. Default 500ms.
	BaseBackoff time.Duration

	// MaxBackoff caps the computed backoff. Default 30s.
	MaxBackoff time.Duration

	// DedupWindow suppresses a repeat of the same event for the same instance
	// within this window (§6.7's anti-spam requirement). Default 30s. Zero
	// disables de-duplication entirely.
	DedupWindow time.Duration

	// Logger receives delivery diagnostics. Default: slog.Default().
	Logger *slog.Logger

	// EventFilter maps a notifier name to the set of events it accepts. A
	// notifier absent from the map receives everything; a notifier mapped to
	// an empty (non-nil) set receives nothing. This is how the settings UI
	// expresses "only send crashes to DingTalk".
	EventFilter map[string][]string
}

// Default option values.
const (
	defaultWorkers     = 4
	defaultQueueSize   = 128
	defaultMaxAttempts = 3
	defaultBaseBackoff = 500 * time.Millisecond
	defaultMaxBackoff  = 30 * time.Second
	defaultDedupWindow = 30 * time.Second
)

// ErrQueueFull reports that a notification was dropped because the dispatcher's
// queue was saturated. Callers can match it with errors.Is to count drops.
var ErrQueueFull = errors.New("notify: dispatcher queue is full")

// ErrNotStarted reports Send on a dispatcher whose Start has not been called.
var ErrNotStarted = errors.New("notify: dispatcher is not started")

// ErrStopped reports Send on a stopped dispatcher.
var ErrStopped = errors.New("notify: dispatcher is stopped")

// NewDispatcher builds a Dispatcher over notifiers.
//
// Passing zero notifiers is allowed — the dispatcher then accepts and discards
// notifications — so the panel does not need to special-case "no channels
// configured".
func NewDispatcher(notifiers []Notifier, opts Options) *Dispatcher {
	if opts.Workers <= 0 {
		opts.Workers = defaultWorkers
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = defaultQueueSize
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = defaultMaxAttempts
	}
	if opts.BaseBackoff <= 0 {
		opts.BaseBackoff = defaultBaseBackoff
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = defaultMaxBackoff
	}
	if opts.DedupWindow < 0 {
		opts.DedupWindow = 0
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	// Drop nil notifiers rather than panicking on them at delivery time: a nil
	// entry is almost always an unconfigured channel slot.
	live := make([]Notifier, 0, len(notifiers))
	for _, n := range notifiers {
		if n != nil {
			live = append(live, n)
		}
	}

	filters := make(map[string]map[string]bool, len(opts.EventFilter))
	for name, events := range opts.EventFilter {
		set := make(map[string]bool, len(events))
		for _, e := range events {
			set[e] = true
		}
		filters[name] = set
	}

	return &Dispatcher{
		notifiers:    live,
		queue:        make(chan job, opts.QueueSize),
		workers:      opts.Workers,
		timeout:      opts.Timeout,
		maxAttempts:  opts.MaxAttempts,
		baseBackoff:  opts.BaseBackoff,
		maxBackoff:   opts.MaxBackoff,
		dedupWindow:  opts.DedupWindow,
		logger:       opts.Logger,
		now:          func() time.Time { return time.Now().UTC() },
		sleep:        sleepCtx,
		rand:         rand.New(rand.NewSource(time.Now().UnixNano())),
		seen:         make(map[string]time.Time),
		stop:         make(chan struct{}),
		results:      make(map[string]Result),
		eventFilters: filters,
	}
}

// Start launches the worker pool. It is idempotent.
func (d *Dispatcher) Start() {
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return
	}
	d.started = true
	d.running = true
	d.mu.Unlock()

	for i := 0; i < d.workers; i++ {
		d.wg.Add(1)
		go d.worker(i)
	}
}

// Send enqueues n for delivery and returns immediately.
//
// It returns:
//
//   - nil when the notification was accepted (not when it was delivered —
//     delivery happens asynchronously and its outcome is recorded in Results);
//   - ErrQueueFull when the queue is saturated (the notification is dropped,
//     counted, and logged);
//   - ErrNotStarted / ErrStopped when the dispatcher is not accepting work.
//
// A notification suppressed by the de-duplication window returns nil: it was
// handled correctly, by not being sent.
func (d *Dispatcher) Send(n Notification) error {
	n = n.Normalize()

	d.mu.Lock()
	switch {
	case !d.started:
		d.mu.Unlock()
		return ErrNotStarted
	case !d.running:
		d.mu.Unlock()
		return ErrStopped
	}
	if d.suppressedLocked(n) {
		d.mu.Unlock()
		d.bumpStats(func(s *Stats) { s.Suppressed++ })
		d.logger.Debug("notification suppressed by dedup window",
			"event", n.Event, "instance", n.Instance)
		return nil
	}
	d.mu.Unlock()

	select {
	case d.queue <- job{notification: n}:
		d.bumpStats(func(s *Stats) { s.Enqueued++ })
		return nil
	default:
		d.bumpStats(func(s *Stats) { s.Dropped++ })
		d.logger.Warn("notification dropped: queue full",
			"event", n.Event, "instance", n.Instance, "queue", cap(d.queue))
		return fmt.Errorf("%w (depth %d, event %s)", ErrQueueFull, cap(d.queue), n.Event)
	}
}

// SendWait enqueues n and blocks until every notifier has finished, returning
// the per-notifier results. It exists for callers that must know the outcome
// (a CLI "send test notification" action, and tests); the supervisor path
// should prefer Send.
//
// It honours ctx: if ctx expires first, SendWait returns the results collected
// so far with ctx.Err(). Delivery continues in the background on its own
// context either way — the notification is not cancelled by a caller giving up,
// because the caller's timeout is not the destination's problem.
func (d *Dispatcher) SendWait(ctx context.Context, n Notification) ([]Result, error) {
	n = n.Normalize()

	d.mu.Lock()
	if !d.started {
		d.mu.Unlock()
		return nil, ErrNotStarted
	}
	if !d.running {
		d.mu.Unlock()
		return nil, ErrStopped
	}
	if d.suppressedLocked(n) {
		d.mu.Unlock()
		d.bumpStats(func(s *Stats) { s.Suppressed++ })
		return nil, nil
	}
	d.mu.Unlock()

	done := make(chan []Result, 1)
	go func() { done <- d.fanOut(context.WithoutCancel(context.Background()), n) }()

	select {
	case res := <-done:
		return res, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Results returns a snapshot of the most recent delivery result per notifier.
func (d *Dispatcher) Results() []Result {
	d.lastMu.Lock()
	defer d.lastMu.Unlock()

	out := make([]Result, 0, len(d.results))
	for _, r := range d.results {
		out = append(out, r)
	}
	return out
}

// Stats returns a counter snapshot.
func (d *Dispatcher) Stats() Stats {
	d.statsMu.Lock()
	defer d.statsMu.Unlock()
	return d.stats
}

// Notifiers returns the configured channel names.
func (d *Dispatcher) Notifiers() []string {
	out := make([]string, 0, len(d.notifiers))
	for _, n := range d.notifiers {
		out = append(out, n.Name())
	}
	return out
}

// Stop drains the queue and waits for in-flight deliveries, bounded by ctx.
//
// It returns ctx.Err() when the drain did not finish in time; the workers are
// abandoned rather than killed, since a goroutine blocked in net/http will
// finish on its own request timeout.
func (d *Dispatcher) Stop(ctx context.Context) error {
	d.mu.Lock()
	if !d.started || !d.running {
		d.running = false
		d.mu.Unlock()
		return nil
	}
	d.running = false
	d.mu.Unlock()

	// Closing stop makes workers finish the queue, then exit.
	close(d.stop)

	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// worker consumes the queue until the dispatcher stops.
func (d *Dispatcher) worker(id int) {
	defer d.wg.Done()

	for {
		select {
		case j := <-d.queue:
			d.fanOut(context.Background(), j.notification)
		case <-d.stop:
			// Drain whatever is already queued so a graceful shutdown does not
			// silently lose the last events (typically a "stopped" notice).
			for {
				select {
				case j := <-d.queue:
					d.fanOut(context.Background(), j.notification)
				default:
					return
				}
			}
		}
	}
}

// fanOut delivers n to every notifier concurrently and returns their results.
func (d *Dispatcher) fanOut(ctx context.Context, n Notification) []Result {
	if len(d.notifiers) == 0 {
		return nil
	}

	results := make([]Result, len(d.notifiers))
	var wg sync.WaitGroup

	for i, notifier := range d.notifiers {
		wg.Add(1)
		go func(i int, notifier Notifier) {
			defer wg.Done()
			results[i] = d.deliverTo(ctx, notifier, n)
		}(i, notifier)
	}
	wg.Wait()

	d.recordResults(results)
	return results
}

// deliverTo performs one notifier's delivery with retries.
//
// It is the only place a Notifier is invoked, which is what makes the
// guarantees about hangs, panics and isolation enforceable in one function.
func (d *Dispatcher) deliverTo(ctx context.Context, notifier Notifier, n Notification) Result {
	name := notifier.Name()
	res := Result{Notifier: name, Event: n.Event}

	if !d.accepts(name, n.Event) {
		res.Skipped = true
		res.At = d.now()
		return res
	}

	var lastErr error
	for attempt := 1; attempt <= d.maxAttempts; attempt++ {
		res.Attempts = attempt

		start := d.now()
		err := d.attempt(ctx, notifier, n)
		res.Duration = d.now().Sub(start)
		res.At = d.now()

		if err == nil {
			res.OK = true
			res.Err = ""
			return res
		}
		lastErr = err

		// Do not sleep after the final attempt.
		if attempt == d.maxAttempts {
			break
		}

		backoff := d.backoff(attempt)
		d.logger.Warn("notification delivery failed; retrying",
			"notifier", name, "event", n.Event, "attempt", attempt,
			"backoff", backoff, "err", err)

		if !d.sleep(ctx, backoff) {
			// Dispatcher shutting down or context cancelled: stop retrying but
			// keep the error so the result is an honest failure.
			lastErr = fmt.Errorf("%w (retry abandoned: %v)", err, ctx.Err())
			break
		}
	}

	res.OK = false
	if lastErr != nil {
		res.Err = lastErr.Error()
	}
	return res
}

// attempt performs a single delivery under a timeout and a panic guard.
func (d *Dispatcher) attempt(ctx context.Context, notifier Notifier, n Notification) (err error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	// A panic in a notifier is a bug in that notifier; it must not take down
	// the dispatcher goroutine (and with it, every other channel's delivery).
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s: notifier panicked: %v", notifier.Name(), r)
			d.logger.Error("notifier panicked",
				"notifier", notifier.Name(), "event", n.Event, "panic", r)
		}
	}()

	// Run the send in its own goroutine so a notifier that ignores the context
	// cannot pin this worker forever. The buffer guarantees the goroutine can
	// always finish and exit even if we stopped waiting.
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("%s: notifier panicked: %v", notifier.Name(), r)
			}
		}()
		done <- notifier.Send(ctx, n)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("%s: %w", notifier.Name(), ctx.Err())
	}
}

// accepts applies the per-notifier event filter.
func (d *Dispatcher) accepts(notifier, event string) bool {
	set, ok := d.eventFilters[notifier]
	if !ok {
		return true // unlisted: receives everything
	}
	return set[event]
}

// suppressedLocked reports whether n falls inside the de-duplication window,
// and records it as seen when it does not.
//
// The key is event + instance, which is exactly the granularity §6.7 asks for:
// "the same event for the same instance within a short window must not spam".
// Two different instances crashing at the same moment are two distinct events
// and both are delivered.
func (d *Dispatcher) suppressedLocked(n Notification) bool {
	if d.dedupWindow <= 0 {
		return false
	}

	key := n.Event + "\x00" + n.Instance
	now := d.now()

	if last, ok := d.seen[key]; ok && now.Sub(last) < d.dedupWindow {
		// Refresh the window only when it has moved on — otherwise a
		// continuously failing instance would suppress its own crash notice
		// forever.
		return true
	}
	d.seen[key] = now

	// Opportunistic cleanup: the map is otherwise unbounded over a long run.
	if len(d.seen) > 1024 {
		for k, t := range d.seen {
			if now.Sub(t) >= d.dedupWindow {
				delete(d.seen, k)
			}
		}
	}
	return false
}

// backoff computes the delay before attempt+1: base * 2^(attempt-1), capped,
// with up to 50% jitter.
//
// Jitter matters because several notifiers failing at once (a network blip)
// would otherwise retry in lockstep and re-hammer the endpoint together.
func (d *Dispatcher) backoff(attempt int) time.Duration {
	shift := attempt - 1
	if shift > 30 {
		shift = 30 // avoid overflowing the shift
	}
	delay := d.baseBackoff << uint(shift)
	if delay > d.maxBackoff || delay <= 0 {
		delay = d.maxBackoff
	}

	d.randMu.Lock()
	jitter := time.Duration(d.rand.Int63n(int64(delay/2) + 1))
	d.randMu.Unlock()

	return delay + jitter
}

// recordResults stores the latest result per notifier and updates the counters.
func (d *Dispatcher) recordResults(results []Result) {
	d.lastMu.Lock()
	for _, r := range results {
		d.results[r.Notifier] = r
	}
	d.lastMu.Unlock()

	var delivered, failed int64
	for _, r := range results {
		switch {
		case r.Skipped:
		case r.OK:
			delivered++
		default:
			failed++
			d.logger.Error("notification delivery failed",
				"notifier", r.Notifier, "event", r.Event,
				"attempts", r.Attempts, "err", r.Err)
		}
	}
	d.bumpStats(func(s *Stats) {
		s.Delivered += delivered
		s.Failed += failed
	})
}

func (d *Dispatcher) bumpStats(f func(*Stats)) {
	d.statsMu.Lock()
	f(&d.stats)
	d.statsMu.Unlock()
}

// sleepCtx sleeps for d, returning false if ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// SetNow overrides the clock. Test-only; exported because the backup and
// scheduler packages' tests share the pattern and a package-internal hook would
// need a different mechanism in each.
func (d *Dispatcher) SetNow(now func() time.Time) {
	if now == nil {
		return
	}
	d.now = now
}

// SetSleep overrides the backoff sleeper. Test-only.
func (d *Dispatcher) SetSleep(sleep func(context.Context, time.Duration) bool) {
	if sleep == nil {
		return
	}
	d.sleep = sleep
}
