package supervisor

import "time"

// EventType enumerates the events published on the global bus (/ws/events).
type EventType string

const (
	// EventState is emitted on every state-machine transition.
	EventState EventType = "state"
	// EventProcessStarted is emitted once the child has been spawned.
	EventProcessStarted EventType = "process.started"
	// EventProcessExited is emitted once the child has been reaped.
	EventProcessExited EventType = "process.exited"
	// EventDegraded is emitted when a PTY was requested but pipes were used.
	EventDegraded EventType = "term.degraded"
	// EventTermMode reports the effective terminal mode the server selected.
	EventTermMode EventType = "term.mode"
	// EventLogError is emitted for `ERROR:` lines (§5.2.1).
	EventLogError EventType = "log.error"
	// EventLogCrash is emitted for crash-marker lines (§5.2.1).
	EventLogCrash EventType = "log.crash"
	// EventPluginLoaded is emitted when a plugin finishes loading.
	EventPluginLoaded EventType = "plugin.loaded"
	// EventSubscriberDropped is emitted when a slow subscriber lost lines.
	EventSubscriberDropped EventType = "log.subscriber.dropped"
)

// Event is the single message shape carried by the global event bus and by
// Options.OnEvent. It is JSON-friendly so the API layer can forward it verbatim.
type Event struct {
	Type EventType `json:"type"`
	ID   int64     `json:"id"`
	TS   time.Time `json:"ts"`

	// From/To are set for EventState.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`

	// Version is the monotonic state version at emission time.
	Version uint64 `json:"version,omitempty"`

	// Message is a human-readable summary.
	Message string `json:"message,omitempty"`

	// State carries a full snapshot for EventState.
	State *Snapshot `json:"state,omitempty"`

	// PID is set for process events.
	PID int `json:"pid,omitempty"`

	// ExitCode is set for EventProcessExited. A pointer so code 0 is visible.
	ExitCode *int `json:"exitCode,omitempty"`

	// Signal is set when the child died from a signal.
	Signal string `json:"signal,omitempty"`

	// Method/Reason describe terminal degradation (EventDegraded) and the stop
	// method that was used (EventProcessExited).
	Method string `json:"method,omitempty"`
	Reason string `json:"reason,omitempty"`

	// Text carries a small payload that is not a log line: the detected
	// terminal mode ("basic"/"enhanced") for EventTermMode.
	Text string `json:"text,omitempty"`

	// Captured anchor values (§5.2.1). These are pointers-free scalars so that
	// a zero value is unambiguous next to an omitted field.
	Port      int    `json:"port,omitempty"`
	WorldName string `json:"worldName,omitempty"`
	Plugin    string `json:"plugin,omitempty"`

	// Rule is the rule name that produced a log event.
	Rule string `json:"rule,omitempty"`
	// Line is the ANSI-stripped text of the triggering line.
	Line string `json:"line,omitempty"`
	// Raw is the ANSI-bearing text, for color replay.
	Raw string `json:"raw,omitempty"`
}

// EventSink receives events. The Manager uses it for its bus; tests use it to
// collect deterministic sequences.
type EventSink func(Event)

// emit publishes an event through Options.OnEventFn.
func (r *Runner) emit(ev Event) {
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	if ev.ID == 0 {
		ev.ID = r.opts.ID
	}
	r.mu.RLock()
	fn := r.opts.OnEventFn
	r.mu.RUnlock()
	if fn != nil {
		fn(ev)
	}
}
