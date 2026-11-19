package session

import (
	"errors"
	"testing"
)

func TestHappyPathTransitions(t *testing.T) {
	m := NewMachine()
	if m.State() != StateIdle {
		t.Fatalf("initial state = %s, want Idle", m.State())
	}

	steps := []struct {
		event Event
		want  State
	}{
		{EventConnect, StateConnecting},
		{EventDescribeOK, StateDescribed},
		{EventSetupOK, StateSetup},
		{EventPlayOK, StatePlaying},
		{EventStop, StateStopping},
		{EventClosed, StateClosed},
	}
	for _, st := range steps {
		if err := m.Transition(st.event); err != nil {
			t.Fatalf("transition %s: %v", st.event, err)
		}
		if m.State() != st.want {
			t.Fatalf("after %s: state = %s, want %s", st.event, m.State(), st.want)
		}
	}

	if m.TransitionCount() != len(steps) {
		t.Fatalf("transition count = %d, want %d", m.TransitionCount(), len(steps))
	}
	if !m.IsTerminal() {
		t.Fatal("machine should be terminal in Closed")
	}

	hist := m.History()
	if len(hist) != len(steps) || hist[0].From != StateIdle || hist[0].To != StateConnecting {
		t.Fatalf("unexpected history: %+v", hist)
	}
}

func TestFailurePathReachesClosed(t *testing.T) {
	m := NewMachine()
	if err := m.Transition(EventConnect); err != nil {
		t.Fatal(err)
	}
	if err := m.Transition(EventFail); err != nil {
		t.Fatalf("fail transition: %v", err)
	}
	if m.State() != StateFailed {
		t.Fatalf("state = %s, want Failed", m.State())
	}
	if err := m.Transition(EventRetryClosed); err != nil {
		t.Fatalf("cleanup transition: %v", err)
	}
	if m.State() != StateClosed || !m.IsTerminal() {
		t.Fatalf("state = %s, want terminal Closed", m.State())
	}
}

func TestIllegalTransitionsAreRejected(t *testing.T) {
	totalRejections := 0
	// Idle cannot jump straight into Describe/Setup/Play/Stop/Closed.
	for _, ev := range []Event{EventDescribeOK, EventSetupOK, EventPlayOK, EventStop, EventClosed} {
		m := NewMachine()
		err := m.Transition(ev)
		var illegal *IllegalTransitionError
		if !errors.As(err, &illegal) {
			t.Fatalf("event %s on Idle: got %v, want IllegalTransitionError", ev, err)
		}
		if illegal.From != StateIdle || illegal.Event != ev {
			t.Fatalf("error locator = %+v, want From=Idle Event=%s", illegal, ev)
		}
		if m.State() != StateIdle {
			t.Fatalf("state changed to %s after rejected transition", m.State())
		}
		totalRejections += m.IllegalRejections()
	}

	// Terminal state rejects everything.
	m := NewMachine()
	_ = m.Transition(EventFail)
	_ = m.Transition(EventRetryClosed)
	if err := m.Transition(EventConnect); err == nil {
		t.Fatal("transition from Closed must be rejected")
	}
	totalRejections += m.IllegalRejections()
	if totalRejections != 6 {
		t.Fatalf("illegal rejection count = %d, want 6", totalRejections)
	}
	if m.TransitionCount() != 2 {
		t.Fatalf("legal transition count = %d, want 2", m.TransitionCount())
	}
}

func TestRuleTableIsSharedAndComplete(t *testing.T) {
	// Every non-terminal state must have at least one legal event.
	for st, rules := range allowedTransitions {
		if st == StateClosed {
			continue
		}
		if len(rules) == 0 {
			t.Fatalf("state %s has no legal transitions", st)
		}
	}
}
