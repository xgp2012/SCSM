// Package notify delivers panel events to external chat/webhook channels
// (plan §6.7).
//
// The design has three layers:
//
//   - Notification is the vendor-neutral message the rest of the panel emits.
//   - Notifier renders one Notification into one vendor's wire format and
//     performs a single HTTP delivery.
//   - Dispatcher fans a Notification out to every configured Notifier
//     concurrently, with a bounded queue, per-target timeouts, retries with
//     backoff, and per-target results.
//
// Two properties matter more than features here, because a notification channel
// must never be able to damage the panel it reports on:
//
//  1. Send never blocks the caller. The caller is usually an instance
//     supervisor reacting to a crash; blocking it on a slow webhook would
//     delay recovery. Dispatcher.Send enqueues and returns.
//  2. One bad channel cannot affect another. Each notifier runs in its own
//     goroutine with its own timeout and its own panic recovery, and the
//     dispatcher's worker pool is bounded so a burst of events cannot spawn
//     unbounded goroutines.
//
// VENDOR PAYLOAD CAVEAT: the JSON shapes below were written from the vendors'
// published robot-webhook documentation, but they have NOT been verified
// against a live endpoint by the author of this package. They are deliberately
// small and isolated in one function per vendor (see dingtalkPayload and
// friends) so that correcting a shape is a one-function change. Confirm each
// against current vendor docs before relying on notifications in production.
package notify

import (
	"context"
	"time"
)

// Event names from §6.7. They are the stable identifiers used for routing,
// filtering and de-duplication; the human-readable text lives in the payload
// builders so that localisation and wording changes never alter an event's
// identity.
const (
	// EventInstanceStarted fires when an instance reached the running state.
	EventInstanceStarted = "instance.started"
	// EventInstanceStopped fires on a clean, requested stop.
	EventInstanceStopped = "instance.stopped"
	// EventInstanceCrashed fires when the process exited without being asked to.
	EventInstanceCrashed = "instance.crashed"
	// EventInstanceRestartFailed fires when automatic restart gave up.
	EventInstanceRestartFailed = "instance.restart.failed"
	// EventBackupCompleted fires after a successful backup.
	EventBackupCompleted = "backup.completed"
	// EventBackupFailed fires when a backup could not be produced.
	EventBackupFailed = "backup.failed"
	// EventDiskLow fires when free disk space crosses the warning threshold.
	EventDiskLow = "disk.low"
	// EventServerReady fires when the server's ready anchor appeared in the log
	// (§5.2.1), which is earlier and more precise than instance.started.
	EventServerReady = "server.ready"
)

// AllEvents lists every event this package knows about, in the order of §6.7.
// It backs the settings UI's channel-per-event matrix and validation of
// configured filters.
func AllEvents() []string {
	return []string{
		EventInstanceStarted,
		EventInstanceStopped,
		EventInstanceCrashed,
		EventInstanceRestartFailed,
		EventBackupCompleted,
		EventBackupFailed,
		EventDiskLow,
		EventServerReady,
	}
}

// ValidEvent reports whether event is one of the §6.7 events.
func ValidEvent(event string) bool {
	for _, e := range AllEvents() {
		if e == event {
			return true
		}
	}
	return false
}

// Severity levels. They are advisory: they colour the message and let a
// channel filter, but they do not change delivery semantics.
const (
	// LevelInfo is routine good news (a server came up, a backup finished).
	LevelInfo = "info"
	// LevelWarn is a recoverable problem the operator should look at.
	LevelWarn = "warn"
	// LevelError is a failure requiring action.
	LevelError = "error"
)

// Notification is the vendor-neutral message handed to a Notifier.
//
// Title/Body are the rendered human-readable text; Event/Instance/Level stay
// machine-readable so a channel can prefix, filter or route on them.
type Notification struct {
	// Event is one of the Event* constants.
	Event string `json:"event"`

	// Title is the short headline, e.g. "实例崩了 / instance crashed".
	Title string `json:"title"`

	// Body is the detail, which may be multi-line.
	Body string `json:"body"`

	// Level is one of LevelInfo/LevelWarn/LevelError.
	Level string `json:"level"`

	// Instance is the instance name, or "" for panel-wide events such as
	// disk.low.
	Instance string `json:"instance,omitempty"`

	// Timestamp is when the event happened (not when it is delivered).
	Timestamp time.Time `json:"timestamp"`

	// Fields carries event-specific extras (path, size, exit code, …). Values
	// are rendered as "key: value" lines by the markdown channels.
	Fields map[string]string `json:"fields,omitempty"`
}

// Notifier delivers Notifications to one destination.
//
// Implementations must be safe for concurrent use: the Dispatcher may invoke
// the same Notifier from several workers at once.
type Notifier interface {
	// Name identifies the channel in logs and delivery results.
	Name() string

	// Send delivers one notification. Returning an error is normal and
	// expected: the Dispatcher records it and decides whether to retry.
	Send(ctx context.Context, n Notification) error
}

// Normalize fills in defaults so every downstream renderer can assume the
// fields are present.
func (n Notification) Normalize() Notification {
	if n.Level == "" {
		n.Level = LevelInfo
	}
	if n.Timestamp.IsZero() {
		n.Timestamp = time.Now().UTC()
	} else {
		n.Timestamp = n.Timestamp.UTC()
	}
	if n.Title == "" {
		n.Title = n.Event
	}
	return n
}

// ensure NonEmpty is a small helper used by the renderers: it returns fallback
// when s is empty.
func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
