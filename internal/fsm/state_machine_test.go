package fsm

import "testing"

func TestHappyPathTransitions(t *testing.T) {
	m := NewMachine()
	steps := []Event{EventAnnounce, EventSetup, EventRecord, EventTeardown, EventClosed}
	for _, ev := range steps {
		if err := m.Handle(ev); err != nil {
			t.Fatalf("event %s: %v", ev, err)
		}
	}
	if m.State() != StateClosed {
		t.Fatalf("state = %s, want Closed", m.State())
	}
	if got := m.TransitionCount(); got != 5 {
		t.Fatalf("transitions = %d, want 5", got)
	}
	wantTrace := []State{StateCreated, StateAnnounced, StateReady, StateRecording, StateClosing, StateClosed}
	if got := m.Trace(); len(got) != len(wantTrace) {
		t.Fatalf("trace = %v, want %v", got, wantTrace)
	}
}

func TestDuplicateEventsDoNotCount(t *testing.T) {
	m := NewMachine()
	_ = m.Handle(EventAnnounce)
	if err := m.Handle(EventAnnounce); err != nil {
		t.Fatalf("duplicate announce should be ignored, got %v", err)
	}
	_ = m.Handle(EventSetup)
	if err := m.Handle(EventSetup); err != nil {
		t.Fatalf("duplicate setup should be ignored, got %v", err)
	}
	_ = m.Handle(EventRecord)
	if err := m.Handle(EventRecord); err != nil {
		t.Fatalf("duplicate record should be ignored, got %v", err)
	}

	if m.TransitionCount() != 3 {
		t.Fatalf("transitions = %d, want 3", m.TransitionCount())
	}
	if m.DuplicateCount() != 3 {
		t.Fatalf("duplicates = %d, want 3", m.DuplicateCount())
	}
	if m.IllegalCount() != 0 {
		t.Fatalf("duplicates must not be counted as illegal, got %d", m.IllegalCount())
	}
}

func TestIllegalTransitions(t *testing.T) {
	m := NewMachine()
	// RECORD is not legal from Created
	if err := m.Handle(EventRecord); err == nil {
		t.Fatal("expected illegal transition error")
	}
	if m.IllegalCount() != 1 {
		t.Fatalf("illegal count = %d, want 1", m.IllegalCount())
	}
	if m.TransitionCount() != 0 {
		t.Fatalf("illegal event must not transition, count = %d", m.TransitionCount())
	}

	// once a transition is rejected the machine still accepts the legal one
	if err := m.Handle(EventAnnounce); err != nil {
		t.Fatalf("legal announce after reject: %v", err)
	}
	if m.State() != StateAnnounced {
		t.Fatalf("state = %s, want Announced", m.State())
	}
}

func TestEarlyTeardownReachesClosed(t *testing.T) {
	m := NewMachine()
	if err := m.Handle(EventTeardown); err != nil {
		t.Fatalf("early teardown: %v", err)
	}
	if m.State() != StateClosing {
		t.Fatalf("state = %s, want Closing", m.State())
	}
	if err := m.Handle(EventClosed); err != nil {
		t.Fatalf("close: %v", err)
	}
	if m.State() != StateClosed || m.TransitionCount() != 2 {
		t.Fatalf("unexpected end state=%s transitions=%d", m.State(), m.TransitionCount())
	}
}
