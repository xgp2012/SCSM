//go:build linux

package supervisor

import (
	"context"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------- state machine

// TestStateTransitions pins the §5.1 transition table, including the illegal
// ones that must be rejected.
func TestStateTransitions(t *testing.T) {
	m := newStateMachine()
	if got := m.stateOf(); got != StateCreated {
		t.Fatalf("initial state = %s, want created", got)
	}
	v0 := m.versionOf()

	// transition(to, allowed...) only fires when the CURRENT state is one of
	// `allowed`. A guard that does not list Created must be ignored.
	if _, ok := m.transition(StateRunning, StateStarting); ok {
		t.Error("transition fired although the current state (Created) was not allowed")
	}
	if got := m.stateOf(); got != StateCreated {
		t.Fatalf("state = %s after a rejected transition, want created", got)
	}
	if _, ok := m.transition(StateStarting, StateCreated); !ok {
		t.Fatal("Created -> Starting was rejected")
	}
	if _, ok := m.transition(StateRunning, StateStarting); !ok {
		t.Fatal("Starting -> Running was rejected")
	}
	if _, ok := m.transition(StateCrashed, StateRunning); !ok {
		t.Fatal("Running -> Crashed was rejected")
	}
	if m.versionOf() <= v0+2 {
		t.Errorf("version did not advance monotonically: %d -> %d", v0, m.versionOf())
	}
}

// TestStateVersionIsMonotonic checks the cheap change-detection counter.
func TestStateVersionIsMonotonic(t *testing.T) {
	m := newStateMachine()
	last := m.versionOf()
	seq := []State{StateStarting, StateRunning, StateStopping, StateStopped, StateStarting, StateRunning}
	for i, s := range seq {
		// No guard: exercise the counter itself.
		up, ok := m.transition(s)
		if !ok {
			t.Fatalf("step %d: transition to %s rejected from %s", i, s, m.stateOf())
		}
		if up.version <= last {
			t.Fatalf("step %d: version %d did not advance past %d", i, up.version, last)
		}
		last = up.version
	}
}

// TestWaitStateAndChangeChannel proves the broadcast wakes waiters.
func TestWaitStateAndChangeChannel(t *testing.T) {
	m := newStateMachine()
	ch := m.changeChan()

	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ch
	}()

	time.Sleep(50 * time.Millisecond)
	m.force(StateStarting)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("change channel was not closed on transition")
	}

	// The next channel must be fresh (not already closed).
	if _, ok := m.transition(StateRunning, StateStarting); !ok {
		t.Fatal("Starting -> Running rejected")
	}
	select {
	case <-m.changeChan():
		// No further transition happened: this must block, so a ready value
		// here means the channel was not replaced correctly.
		t.Error("change channel was not replaced after the transition")
	default:
	}
}

// TestWaitStateContext covers the exported waiting helpers.
func TestWaitStateContext(t *testing.T) {
	r, err := NewRunner(helperOpts(t, "ready"))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	err = r.WaitState(ctx, func(s State) bool { return s == StateRunning })
	if err == nil {
		t.Fatal("WaitState returned nil despite the target state never being reached")
	}
	// The predicate already holding must return immediately even with a dead ctx.
	if err := r.WaitState(ctx, func(s State) bool { return s == StateCreated }); err != nil {
		t.Errorf("WaitState with a satisfied predicate = %v, want nil", err)
	}
}

// TestParseStateRoundTrip documents the persistence helper.
func TestParseStateRoundTrip(t *testing.T) {
	for _, s := range []State{StateCreated, StateStarting, StateRunning, StateStopping, StateStopped, StateCrashed, StateFailed} {
		got, err := ParseState(s.String())
		if err != nil || got != s {
			t.Errorf("ParseState(%q) = %v, %v", s, got, err)
		}
	}
	if _, err := ParseState("exploded"); err == nil {
		t.Error("ParseState accepted a bogus state")
	}
}

// TestStateHelpers covers Active/Terminal/Ready predicates.
func TestStateHelpers(t *testing.T) {
	if !StateRunning.Active() || !StateStarting.Active() || !StateStopping.Active() {
		t.Error("Active() must cover starting/running/stopping")
	}
	if StateStopped.Active() || StateCrashed.Active() || StateFailed.Active() || StateCreated.Active() {
		t.Error("Active() must not cover terminal or created states")
	}
	if !StateStopped.Terminal() || !StateCrashed.Terminal() || !StateFailed.Terminal() {
		t.Error("Terminal() must cover stopped/crashed/failed")
	}
	if StateRunning.Terminal() || StateCreated.Terminal() {
		t.Error("Terminal() must not cover running or created")
	}
	// Ready needs the full predicate, not just the state.
	s := Snapshot{State: StateRunning, Alive: true, Listening: true, WorldDone: false}
	if s.Ready() {
		t.Error("Ready() true without the Game screen anchor")
	}
	s.WorldDone = true
	if !s.Ready() {
		t.Error("Ready() false with all three conditions met")
	}
}

// TestStateMachineConcurrent exercises the lock under -race.
func TestStateMachineConcurrent(t *testing.T) {
	m := newStateMachine()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				switch j % 4 {
				case 0:
					m.setAlive(true)
				case 1:
					m.setPort(1000 + j)
				case 2:
					m.setWorld("w")
				default:
					_ = m.snapshot()
					_ = m.versionOf()
					m.readyNow()
				}
			}
		}(i)
	}
	wg.Wait()
}

// TestSnapshotCarriesExitCodePointer proves exit code 0 is distinguishable from
// "no exit code".
func TestSnapshotCarriesExitCodePointer(t *testing.T) {
	m := newStateMachine()
	if s := m.snapshot(); s.ExitCode != nil {
		t.Error("ExitCode should be nil before any exit")
	}
	zero := 0
	m.setExitCode(&zero)
	s := m.snapshot()
	if s.ExitCode == nil {
		t.Fatal("ExitCode pointer lost")
	}
	if *s.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", *s.ExitCode)
	}
	// Mutating the source must not change the snapshot copy.
	zero = 42
	if *s.ExitCode != 0 {
		t.Error("Snapshot aliased the caller's exit-code variable")
	}
}
