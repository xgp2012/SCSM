package api

import (
	"encoding/json"
	"sync"
	"time"
)

// Event types published on /ws/events (§5.6 "全局事件（状态变更/告警）").
const (
	// EventStateChange fires when an instance's lifecycle state changes.
	EventStateChange = "state_change"
	// EventAlert fires for operator-visible warnings (crash, disk low).
	EventAlert = "alert"
	// EventAudit fires after a mutating operation, mirroring the audit row.
	EventAudit = "audit"
	// EventInstance fires for instance CRUD (created/updated/deleted).
	EventInstance = "instance"
	// EventBackup fires when a backup completes or fails.
	EventBackup = "backup"
	// EventReady is sent to a client immediately on connect so it can
	// distinguish "no events yet" from "socket broken".
	EventReady = "ready"
	// EventPing is a server-initiated keepalive frame.
	EventPing = "ping"
)

// Event is one message on the global event stream.
type Event struct {
	// Type is one of the Event* constants.
	Type string `json:"type"`
	// InstanceID scopes the event, 0 for panel-wide events.
	InstanceID int64 `json:"instance_id,omitempty"`
	// Timestamp is when the event was produced.
	Timestamp time.Time `json:"ts"`
	// Data is the event-specific payload.
	Data any `json:"data,omitempty"`
}

// EventHub is the EventBroker implementation: an in-process fan-out.
//
// Delivery is best-effort and non-blocking. A subscriber whose buffer is full
// has its *oldest* event dropped rather than stalling the publisher, because
// the publisher may be a supervisor goroutine handling a state transition and
// must never block on a slow browser.
type EventHub struct {
	log Logger

	mu     sync.RWMutex
	subs   map[*EventSubscription]struct{}
	closed bool
}

// NewEventHub builds an empty hub.
func NewEventHub(log Logger) *EventHub {
	if log == nil {
		log = NewDiscardLogger()
	}
	return &EventHub{log: log, subs: make(map[*EventSubscription]struct{})}
}

// Compile-time assertion.
var _ EventBroker = (*EventHub)(nil)

// Publish delivers ev to every subscriber.
func (h *EventHub) Publish(ev Event) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}

	h.mu.RLock()
	if h.closed {
		h.mu.RUnlock()
		return
	}
	subs := make([]*EventSubscription, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.RUnlock()

	for _, s := range subs {
		s.push(ev)
	}
}

// Subscribe registers a new subscriber.
func (h *EventHub) Subscribe() *EventSubscription {
	h.mu.Lock()
	defer h.mu.Unlock()

	sub := &EventSubscription{
		events: make(chan Event, 256),
		hub:    h,
		done:   make(chan struct{}),
	}
	if h.closed {
		close(sub.events)
		close(sub.done)
		return sub
	}
	h.subs[sub] = struct{}{}
	return sub
}

// SubscriberCount reports the number of live subscribers.
func (h *EventHub) SubscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

// Close shuts the hub down and closes every subscriber channel.
func (h *EventHub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	subs := make([]*EventSubscription, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.subs = make(map[*EventSubscription]struct{})
	h.mu.Unlock()

	for _, s := range subs {
		s.close()
	}
}

func (h *EventHub) remove(s *EventSubscription) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
}

// EventSubscription is one /ws/events connection's queue.
type EventSubscription struct {
	events chan Event
	hub    *EventHub

	once sync.Once
	done chan struct{}
}

// Events returns the receive channel. It is closed when the subscription ends.
func (s *EventSubscription) Events() <-chan Event { return s.events }

// Done is closed when the subscription ends.
func (s *EventSubscription) Done() <-chan struct{} { return s.done }

// Close unregisters the subscription. Safe to call repeatedly.
func (s *EventSubscription) Close() {
	s.hub.remove(s)
	s.close()
}

func (s *EventSubscription) close() {
	s.once.Do(func() {
		close(s.done)
		close(s.events)
	})
}

// push enqueues an event, dropping the oldest queued event when full so the
// newest state is always delivered.
func (s *EventSubscription) push(ev Event) {
	select {
	case <-s.done:
		return
	default:
	}

	select {
	case s.events <- ev:
		return
	default:
	}

	// Full: drop the oldest and retry once. If the retry also fails another
	// goroutine raced us and the channel is still full; dropping this event is
	// preferable to blocking the publisher.
	select {
	case <-s.events:
	default:
	}
	select {
	case s.events <- ev:
	default:
	}
}

// ---------------------------------------------------------------------------
// BroadcastLogHub
// ---------------------------------------------------------------------------

// BroadcastLogHub is a LogBroadcaster that fans per-instance log lines out to
// any interested subscriber.
//
// The console WebSocket gets its feed from ProcessManager.Subscribe (which is
// authoritative and supports replay), so this hub exists for *additional*
// consumers: a future server-sent-events dashboard, the scheduler's crash
// detector, notification triggers. It is wired as Deps.LogsHub.
type BroadcastLogHub struct {
	log Logger

	mu   sync.RWMutex
	subs map[int64]map[*LogFanout]struct{}
}

// NewBroadcastLogHub builds an empty hub.
func NewBroadcastLogHub(log Logger) *BroadcastLogHub {
	if log == nil {
		log = NewDiscardLogger()
	}
	return &BroadcastLogHub{log: log, subs: make(map[int64]map[*LogFanout]struct{})}
}

// Compile-time assertion.
var _ LogBroadcaster = (*BroadcastLogHub)(nil)

// PublishLog delivers a line to subscribers of instanceID.
func (h *BroadcastLogHub) PublishLog(instanceID int64, line LogLine) {
	h.mu.RLock()
	subs := make([]*LogFanout, 0, len(h.subs[instanceID]))
	for s := range h.subs[instanceID] {
		subs = append(subs, s)
	}
	h.mu.RUnlock()

	for _, s := range subs {
		s.push(line)
	}
}

// Subscribe returns a fan-out for one instance.
func (h *BroadcastLogHub) Subscribe(instanceID int64) *LogFanout {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.subs[instanceID] == nil {
		h.subs[instanceID] = make(map[*LogFanout]struct{})
	}
	f := &LogFanout{
		id:    instanceID,
		lines: make(chan LogLine, 256),
		hub:   h,
		done:  make(chan struct{}),
	}
	h.subs[instanceID][f] = struct{}{}
	return f
}

func (h *BroadcastLogHub) remove(id int64, f *LogFanout) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m, ok := h.subs[id]; ok {
		delete(m, f)
		if len(m) == 0 {
			delete(h.subs, id)
		}
	}
}

// LogFanout is one subscriber of an instance's log stream.
type LogFanout struct {
	id    int64
	lines chan LogLine
	hub   *BroadcastLogHub

	once sync.Once
	done chan struct{}
}

// Lines returns the receive channel.
func (f *LogFanout) Lines() <-chan LogLine { return f.lines }

// Close unregisters the fan-out.
func (f *LogFanout) Close() {
	f.hub.remove(f.id, f)
	f.once.Do(func() {
		close(f.done)
		close(f.lines)
	})
}

func (f *LogFanout) push(l LogLine) {
	select {
	case <-f.done:
		return
	default:
	}
	select {
	case f.lines <- l:
	default:
		// Drop when the consumer is behind; logs are lossy by nature here.
	}
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

// toJSONString renders v as compact JSON, falling back to a quoted
// representation when marshalling fails (a non-marshalable detail must not
// break an audit write or an API response).
func toJSONString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "\"<unmarshalable>\""
	}
	return string(b)
}
