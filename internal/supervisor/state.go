package supervisor

import (
	"fmt"
	"sync"
	"time"
)

// State is the lifecycle state of one instance (§5.1).
//
//	Created ──start──► Starting ──anchors+liveness──► Running ──stop──► Stopping ──► Stopped
//	                       │                              │
//	                       └──timeout──► Failed           └──unexpected exit──► Crashed
type State string

const (
	StateCreated  State = "created"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateStopped  State = "stopped"
	StateCrashed  State = "crashed"
	StateFailed   State = "failed"
)

// Active reports whether the instance occupies a process slot.
func (s State) Active() bool {
	switch s {
	case StateStarting, StateRunning, StateStopping:
		return true
	default:
		return false
	}
}

// Terminal reports whether the state is an end state.
func (s State) Terminal() bool {
	switch s {
	case StateStopped, StateCrashed, StateFailed:
		return true
	default:
		return false
	}
}

func (s State) String() string { return string(s) }

// ParseState converts a persisted/external string back into a State.
func ParseState(s string) (State, error) {
	switch st := State(s); st {
	case StateCreated, StateStarting, StateRunning, StateStopping, StateStopped, StateCrashed, StateFailed:
		return st, nil
	default:
		return "", fmt.Errorf("supervisor: unknown state %q", s)
	}
}

// Snapshot is an immutable view of the state machine plus the readiness
// evidence the UI needs to explain "why not ready yet".
type Snapshot struct {
	State   State     `json:"state"`
	Version uint64    `json:"version"`
	Since   time.Time `json:"since"`
	PID     int       `json:"pid"`

	// Readiness evidence (§5.1): all three must hold.
	Alive     bool `json:"alive"`
	Listening bool `json:"listening"`
	WorldDone bool `json:"worldDone"`

	// Captured anchor values.
	Port      int    `json:"port,omitempty"`
	WorldName string `json:"worldName,omitempty"`

	// LastError holds the failure reason for Failed/Crashed.
	LastError string `json:"lastError,omitempty"`
	ExitCode  *int   `json:"exitCode,omitempty"`
	Restarts  int    `json:"restarts"`

	// Method is the effective I/O mode: "pty" or "pipe".
	Method string `json:"method"`
	// Degraded is set when a PTY was requested but pipes are in use.
	Degraded       bool   `json:"degraded"`
	DegradedReason string `json:"degradedReason,omitempty"`
}

// Ready reports the §5.1 readiness predicate.
func (s Snapshot) Ready() bool {
	return s.State == StateRunning && s.Alive && s.Listening && s.WorldDone
}

// stateMachine owns the state, the readiness anchors and the change broadcast.
//
// Lock ordering: stateMachine.mu is a leaf lock. It is never held while taking
// Runner.mu or LogPipe locks.
type stateMachine struct {
	mu      sync.Mutex
	state   State
	version uint64
	since   time.Time

	alive     bool
	listening bool
	worldDone bool
	port      int
	worldName string

	lastErr  string
	exitCode *int
	restarts int

	changes chan struct{} // closed-and-replaced on every transition
	waiters []chan struct{}
}

func newStateMachine() *stateMachine {
	return &stateMachine{
		state:   StateCreated,
		version: 1,
		since:   time.Now(),
		changes: make(chan struct{}),
	}
}

// stateUpdate describes a single application of a transition.
type stateUpdate struct {
	from, to State
	version  uint64
	at       time.Time
}

// transition applies to if the current state is one of allowed. It returns the
// update and true when the state actually changed.
func (m *stateMachine) transition(to State, allowed ...State) (stateUpdate, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(allowed) > 0 {
		ok := false
		for _, a := range allowed {
			if m.state == a {
				ok = true
				break
			}
		}
		if !ok {
			return stateUpdate{}, false
		}
	}
	if m.state == to {
		return stateUpdate{}, false
	}
	up := stateUpdate{from: m.state, to: to, at: time.Now()}
	m.state = to
	m.version++
	m.since = up.at
	up.version = m.version
	m.broadcastLocked()
	return up, true
}

// force sets the state regardless of the current value (used by reset).
func (m *stateMachine) force(to State) stateUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()
	up := stateUpdate{from: m.state, to: to, at: time.Now()}
	m.state = to
	m.version++
	m.since = up.at
	up.version = m.version
	m.broadcastLocked()
	return up
}

// setAlive updates liveness and reports whether readiness flipped to true.
func (m *stateMachine) setAlive(v bool) {
	m.mu.Lock()
	m.alive = v
	m.mu.Unlock()
}

func (m *stateMachine) setPort(p int) {
	m.mu.Lock()
	m.port = p
	m.listening = true
	m.mu.Unlock()
}

func (m *stateMachine) setWorld(name string) {
	m.mu.Lock()
	m.worldName = name
	m.worldDone = true
	m.mu.Unlock()
}

// readyNow reports whether all three readiness conditions hold. The caller
// still has to check that the process is in Starting.
func (m *stateMachine) readyNow() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.alive && m.listening && m.worldDone
}

func (m *stateMachine) setFailure(msg string, code *int) {
	m.mu.Lock()
	m.lastErr = msg
	if code != nil {
		m.exitCode = code
	}
	m.mu.Unlock()
}

func (m *stateMachine) setExitCode(code *int) {
	m.mu.Lock()
	m.exitCode = code
	m.mu.Unlock()
}

func (m *stateMachine) incRestarts() {
	m.mu.Lock()
	m.restarts++
	m.mu.Unlock()
}

// clearReadiness resets the anchors for a fresh start.
func (m *stateMachine) clearReadiness() {
	m.mu.Lock()
	m.alive = false
	m.listening = false
	m.worldDone = false
	m.port = 0
	m.worldName = ""
	m.exitCode = nil
	m.mu.Unlock()
}

func (m *stateMachine) snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Snapshot{
		State:     m.state,
		Version:   m.version,
		Since:     m.since,
		Alive:     m.alive,
		Listening: m.listening,
		WorldDone: m.worldDone,
		Port:      m.port,
		WorldName: m.worldName,
		LastError: m.lastErr,
		Restarts:  m.restarts,
	}
	if m.exitCode != nil {
		c := *m.exitCode
		s.ExitCode = &c
	}
	return s
}

func (m *stateMachine) stateOf() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// version returns the monotonic transition counter. Every state change bumps
// it, so callers can use it for cheap change detection.
func (m *stateMachine) versionOf() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.version
}

// broadcastLocked closes the current change channel and installs a fresh one.
// Callers hold m.mu.
func (m *stateMachine) broadcastLocked() {
	close(m.changes)
	ch := make(chan struct{})
	m.changes = ch
	m.waiters = nil
}

// changeChan returns a channel closed on the next transition.
func (m *stateMachine) changeChan() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.changes
}

// transitionEvent builds an Event describing a state change.
func (r *Runner) transitionEvent(up stateUpdate) Event {
	snap := r.Snapshot()
	ev := Event{
		Type:      EventState,
		ID:        r.opts.ID,
		TS:        up.at,
		From:      up.from.String(),
		To:        up.to.String(),
		Version:   up.version,
		State:     &snap,
		Message:   fmt.Sprintf("%s -> %s", up.from, up.to),
		Port:      snap.Port,
		WorldName: snap.WorldName,
	}
	return ev
}
