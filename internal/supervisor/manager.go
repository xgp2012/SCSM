package supervisor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Manager owns the instanceID -> *Runner registry and the global event bus the
// API layer's /ws/events endpoint subscribes to.
//
// Every exported method is safe for concurrent use (verified under -race).
type Manager struct {
	mu      sync.RWMutex
	runners map[int64]*Runner
	closed  bool

	bus     *EventBus
	factory func(Options) (*Runner, error)
}

// NewManager returns an empty manager with its own event bus.
func NewManager() *Manager {
	m := &Manager{
		runners: make(map[int64]*Runner),
		bus:     NewEventBus(DefaultEventBuffer),
	}
	m.factory = func(o Options) (*Runner, error) {
		return NewRunnerWithSink(o, m.bus.Publish)
	}
	return m
}

// SetFactory overrides how runners are constructed. Tests use it to inject
// runners with extra hooks; production callers should not need it.
func (m *Manager) SetFactory(f func(Options) (*Runner, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.factory = f
}

// Add creates a runner for opts (and starts it when start is true).
func (m *Manager) Add(ctx context.Context, opts Options, start bool) (*Runner, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if _, exists := m.runners[opts.ID]; exists {
		m.mu.Unlock()
		return nil, fmt.Errorf("supervisor: instance %d already registered", opts.ID)
	}
	factory := m.factory
	m.mu.Unlock()

	r, err := factory(opts)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	m.runners[opts.ID] = r
	m.mu.Unlock()

	if start {
		if err := r.Start(ctx); err != nil {
			// Registration survives a failed start: the instance still exists
			// and its Failed state is exactly what the UI must show.
			return r, err
		}
	}
	return r, nil
}

// Register inserts an already-built runner (used when restoring persisted
// instances).
func (m *Manager) Register(r *Runner) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if _, exists := m.runners[r.ID()]; exists {
		return fmt.Errorf("supervisor: instance %d already registered", r.ID())
	}
	m.runners[r.ID()] = r
	return nil
}

// Get returns the runner for id.
func (m *Manager) Get(id int64) (*Runner, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.runners[id]
	return r, ok
}

// Remove stops the instance (if running) and unregisters it.
func (m *Manager) Remove(ctx context.Context, id int64) error {
	m.mu.Lock()
	r, ok := m.runners[id]
	if ok {
		delete(m.runners, id)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("supervisor: instance %d not found", id)
	}
	if r.State().Active() {
		if _, err := r.Stop(ctx); err != nil && !errors.Is(err, ErrAlreadyStopped) {
			return err
		}
	}
	return nil
}

// List returns the runners sorted by id, so API responses are stable.
func (m *Manager) List() []*Runner {
	m.mu.RLock()
	out := make([]*Runner, 0, len(m.runners))
	for _, r := range m.runners {
		out = append(out, r)
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// Snapshots returns the state of every registered instance.
func (m *Manager) Snapshots() []Snapshot {
	rs := m.List()
	out := make([]Snapshot, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Snapshot())
	}
	return out
}

// IDs returns the registered instance ids, sorted.
func (m *Manager) IDs() []int64 {
	rs := m.List()
	out := make([]int64, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ID())
	}
	return out
}

// Start starts the instance registered under id.
func (m *Manager) Start(ctx context.Context, id int64) error {
	r, err := m.mustGet(id)
	if err != nil {
		return err
	}
	return r.Start(ctx)
}

// Stop runs the stop ladder for the instance registered under id.
func (m *Manager) Stop(ctx context.Context, id int64) (StopResult, error) {
	r, err := m.mustGet(id)
	if err != nil {
		return StopResult{}, err
	}
	return r.Stop(ctx)
}

// Restart restarts the instance registered under id.
func (m *Manager) Restart(ctx context.Context, id int64) error {
	r, err := m.mustGet(id)
	if err != nil {
		return err
	}
	return r.Restart(ctx)
}

// SendCommand forwards a console command to an instance.
func (m *Manager) SendCommand(id int64, line string) error {
	r, err := m.mustGet(id)
	if err != nil {
		return err
	}
	return r.SendCommand(line)
}

// Subscribe attaches a log subscription for one instance.
func (m *Manager) Subscribe(ctx context.Context, id int64, replayLines int, raw bool) (*Subscription, error) {
	r, err := m.mustGet(id)
	if err != nil {
		return nil, err
	}
	return r.Subscribe(ctx, replayLines, raw)
}

// Metrics samples one instance.
func (m *Manager) Metrics(id int64) (Metrics, error) {
	r, err := m.mustGet(id)
	if err != nil {
		return Metrics{}, err
	}
	return r.Sample()
}

func (m *Manager) mustGet(id int64) (*Runner, error) {
	r, ok := m.Get(id)
	if !ok {
		return nil, fmt.Errorf("supervisor: instance %d not found", id)
	}
	return r, nil
}

// Bus exposes the global event bus for /ws/events.
func (m *Manager) Bus() *EventBus { return m.bus }

// Events returns the global event channel. The bus fans out to every caller of
// this method; a slow consumer is dropped rather than allowed to block others.
func (m *Manager) Events(ctx context.Context, buffer int) *EventSubscription {
	return m.bus.Subscribe(ctx, buffer)
}

// Count returns the number of registered instances.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.runners)
}

// Close stops every instance and shuts the bus down. Safe to call twice.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	runners := make([]*Runner, 0, len(m.runners))
	for _, r := range m.runners {
		runners = append(runners, r)
	}
	m.runners = map[int64]*Runner{}
	m.mu.Unlock()

	var firstErr error
	for _, r := range runners {
		if !r.State().Active() {
			continue
		}
		if _, err := r.Stop(ctx); err != nil && !errors.Is(err, ErrAlreadyStopped) && firstErr == nil {
			firstErr = err
		}
	}
	m.bus.Close()
	return firstErr
}

// ------------------------------------------------------------------ event bus

// DefaultEventBuffer is the per-subscriber queue depth of the global bus.
const DefaultEventBuffer = 256

// EventBus is a broadcast bus for control-plane events. Subscribers get their
// own buffered channel; publishing never blocks on a slow consumer.
type EventBus struct {
	mu     sync.RWMutex
	subs   map[*EventSubscription]struct{}
	closed bool

	published atomic.Uint64
	dropped   atomic.Uint64
	lastSeq   atomic.Uint64
}

// EventSubscription is one subscriber's view of the bus.
//
// Publish is called from many runner goroutines, so every mutable field here
// must be goroutine-safe: seq is atomic, and close is guarded by sync.Once.
type EventSubscription struct {
	bus   *EventBus
	ch    chan Event
	seq   atomic.Uint64
	close sync.Once
	done  chan struct{}
}

// NewEventBus returns an empty bus.
func NewEventBus(_ int) *EventBus {
	return &EventBus{subs: make(map[*EventSubscription]struct{})}
}

// Publish delivers ev to every subscriber. It never blocks: a subscriber whose
// buffer is full loses the event (and the bus counts the drop).
func (b *EventBus) Publish(ev Event) {
	b.mu.RLock()
	if b.closed || len(b.subs) == 0 {
		b.mu.RUnlock()
		return
	}
	subs := make([]*EventSubscription, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.RUnlock()

	b.published.Add(1)
	seq := b.lastSeq.Add(1)
	for _, s := range subs {
		s.deliver(seq, ev)
	}
}

// Seq returns the sequence number of the most recently delivered event.
func (s *EventSubscription) Seq() uint64 { return s.seq.Load() }

func (s *EventSubscription) deliver(seq uint64, ev Event) {
	select {
	case <-s.done:
		return
	default:
	}
	select {
	case s.ch <- ev:
		s.seq.Store(seq)
	default:
		s.bus.dropped.Add(1)
	}
}

// Subscribe returns a subscription. When ctx is cancelled the subscription is
// closed automatically, which is what the WebSocket handler wants.
func (b *EventBus) Subscribe(ctx context.Context, buffer int) *EventSubscription {
	if buffer <= 0 {
		buffer = DefaultEventBuffer
	}
	s := &EventSubscription{
		bus:  b,
		ch:   make(chan Event, buffer),
		done: make(chan struct{}),
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(s.done)
		return s
	}
	b.subs[s] = struct{}{}
	b.mu.Unlock()

	if ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				s.Close()
			case <-s.done:
			}
		}()
	}
	return s
}

// Ch is the event stream.
func (s *EventSubscription) Ch() <-chan Event { return s.ch }

// Close unregisters the subscription. Idempotent.
func (s *EventSubscription) Close() {
	s.close.Do(func() {
		close(s.done)
		s.bus.mu.Lock()
		delete(s.bus.subs, s)
		s.bus.mu.Unlock()
	})
}

// Stats reports bus counters for a diagnostics endpoint.
func (b *EventBus) Stats() (subscribers int, published, dropped uint64) {
	b.mu.RLock()
	n := len(b.subs)
	b.mu.RUnlock()
	return n, b.published.Load(), b.dropped.Load()
}

// Close shuts the bus and closes every subscriber channel.
func (b *EventBus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := make([]*EventSubscription, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.subs = map[*EventSubscription]struct{}{}
	b.mu.Unlock()
	for _, s := range subs {
		s.Close()
	}
}

// WaitEvent waits for the next event or ctx expiry. Helper for tests and for
// the API's synchronous "wait for state X" endpoints.
func (s *EventSubscription) WaitEvent(ctx context.Context, pred func(Event) bool) (Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case ev, ok := <-s.ch:
			if !ok {
				return Event{}, ErrClosed
			}
			if pred == nil || pred(ev) {
				return ev, nil
			}
		case <-ctx.Done():
			return Event{}, ctx.Err()
		}
	}
}

// WaitForState is a convenience for the API layer: block until an instance
// reaches want, or the bus reports a terminal state.
func (s *EventSubscription) WaitForState(ctx context.Context, id int64, want State) (Event, error) {
	return s.WaitEvent(ctx, func(ev Event) bool {
		if ev.Type != EventState || ev.ID != id {
			return false
		}
		return ev.To == want.String()
	})
}

var _ = time.Second
