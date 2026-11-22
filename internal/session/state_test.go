package session

import "testing"

func TestHappyPathTransitions(t *testing.T) {
	m := NewMachine()

	steps := []struct {
		ev   Event
		want State
	}{
		{Event{Method: MethodOptions, OK: true}, StateOptions},
		{Event{Method: MethodDescribe, OK: true}, StateDescribed},
		{Event{Method: MethodSetup, OK: true}, StateSetup},
		{Event{Method: MethodPlay, OK: true}, StatePlaying},
		{Event{Method: MethodTeardown, OK: true}, StateTeardown},
	}
	for _, st := range steps {
		if err := m.Apply(st.ev); err != nil {
			t.Fatalf("Apply(%+v) unexpected error: %v", st.ev, err)
		}
		if m.Current() != st.want {
			t.Fatalf("state = %s, want %s", m.Current(), st.want)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if m.Current() != StateClosed {
		t.Fatalf("state = %s, want Closed", m.Current())
	}
	// Options -> Described -> Setup -> Playing -> Teardown -> Closed
	if got := m.TransitionCount(); got != 6 {
		t.Fatalf("transition count = %d, want 6", got)
	}
	if m.Failed() {
		t.Fatal("machine should not be failed")
	}
}

func TestIllegalOrderFailsAndKeepsStage(t *testing.T) {
	m := NewMachine()
	if err := m.Apply(Event{Method: MethodOptions, OK: true}); err != nil {
		t.Fatal(err)
	}
	// PLAY is illegal in the Options state.
	err := m.Apply(Event{Method: MethodPlay, OK: true})
	if err == nil {
		t.Fatal("expected error for illegal PLAY")
	}
	if m.Current() != StateFailed {
		t.Fatalf("state = %s, want Failed", m.Current())
	}
	if m.FailedStage() != StateOptions {
		t.Fatalf("failed stage = %s, want Options", m.FailedStage())
	}
	if m.FailedMethod() != MethodPlay {
		t.Fatalf("failed method = %s, want PLAY", m.FailedMethod())
	}
	// The machine stays failed: later events are rejected.
	if err := m.Apply(Event{Method: MethodDescribe, OK: true}); err == nil {
		t.Fatal("expected error when applying event to failed machine")
	}
	// The one legal transition before the failure is still observable.
	if got := m.TransitionCount(); got != 1 {
		t.Fatalf("transition count = %d, want 1", got)
	}
}

func TestFailedResponseFails(t *testing.T) {
	m := NewMachine()
	err := m.Apply(Event{Method: MethodDescribe, OK: false})
	if err == nil {
		t.Fatal("expected error for non-2xx response")
	}
	if m.Current() != StateFailed || m.FailedStage() != StateInit {
		t.Fatalf("got state %s stage %s, want Failed/Init", m.Current(), m.FailedStage())
	}
}

func TestTransportErrorFails(t *testing.T) {
	m := NewMachine()
	_ = m.Apply(Event{Method: MethodOptions, OK: true})
	err := m.Apply(Event{Method: MethodDescribe, OK: true, TransportErr: true})
	if err == nil {
		t.Fatal("expected transport error")
	}
	if m.Current() != StateFailed || m.FailedStage() != StateOptions {
		t.Fatalf("got state %s stage %s, want Failed/Options", m.Current(), m.FailedStage())
	}
}

func TestCloseBeforeTeardownFails(t *testing.T) {
	m := NewMachine()
	_ = m.Apply(Event{Method: MethodOptions, OK: true})
	if err := m.Close(); err == nil {
		t.Fatal("expected error closing before TEARDOWN")
	}
	if m.Current() != StateFailed {
		t.Fatalf("state = %s, want Failed", m.Current())
	}
}
