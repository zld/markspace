package fsm

import (
	"fmt"
	"sync"
)

// Event is a control-plane event derived from an RTSP request or from the
// connection lifecycle.
type Event int

const (
	// EventAnnounce corresponds to an accepted ANNOUNCE request.
	EventAnnounce Event = iota
	// EventSetup corresponds to an accepted SETUP request.
	EventSetup
	// EventRecord corresponds to an accepted RECORD request.
	EventRecord
	// EventTeardown corresponds to TEARDOWN or an observed connection close.
	EventTeardown
	// EventClosed marks the completion of the connection teardown.
	EventClosed
)

// String implements fmt.Stringer.
func (e Event) String() string {
	switch e {
	case EventAnnounce:
		return "Announce"
	case EventSetup:
		return "Setup"
	case EventRecord:
		return "Record"
	case EventTeardown:
		return "Teardown"
	case EventClosed:
		return "Closed"
	default:
		return fmt.Sprintf("Unknown(%d)", int(e))
	}
}

// transitions maps (state, event) to the next state. A missing entry means
// the transition is illegal.
//
// Created -> Announced -> Ready -> Recording -> Closing -> Closed
var transitions = map[State]map[Event]State{
	StateCreated: {
		EventAnnounce: StateAnnounced,
		// a connection that drops before ANNOUNCE still tears down cleanly
		EventTeardown: StateClosing,
	},
	StateAnnounced: {
		EventSetup:    StateReady,
		EventTeardown: StateClosing,
	},
	StateReady: {
		EventRecord:   StateRecording,
		EventTeardown: StateClosing,
	},
	StateRecording: {
		EventTeardown: StateClosing,
	},
	StateClosing: {
		EventClosed: StateClosed,
	},
	StateClosed: {},
}

// Machine is a goroutine-safe RTSP control-plane state machine.
type Machine struct {
	mu                 sync.Mutex
	state              State
	transitions        int
	illegalTransitions int
	duplicateEvents    int
	trace              []State
}

// NewMachine returns a machine in StateCreated.
func NewMachine() *Machine {
	return &Machine{state: StateCreated, trace: []State{StateCreated}}
}

// Handle applies an event.
//
// Re-dispatching the event that already produced the current state is a
// duplicate: it is counted but neither changes the state nor increments the
// transition counter. Any other event that is not allowed from the current
// state is an illegal transition and returns an error.
func (m *Machine) Handle(ev Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if next, ok := transitions[m.state][ev]; ok {
		m.state = next
		m.transitions++
		m.trace = append(m.trace, next)
		return nil
	}

	if m.duplicatesCurrent(ev) {
		m.duplicateEvents++
		return nil
	}

	m.illegalTransitions++
	return fmt.Errorf("illegal transition: event %s not allowed in state %s", ev, m.state)
}

// duplicatesCurrent reports whether ev is the event that was used to enter
// the current state. Re-announce, re-setup and re-record requests therefore
// collapse into no-ops instead of errors.
func (m *Machine) duplicatesCurrent(ev Event) bool {
	switch m.state {
	case StateAnnounced:
		return ev == EventAnnounce
	case StateReady:
		return ev == EventSetup
	case StateRecording:
		return ev == EventRecord
	case StateClosing:
		return ev == EventTeardown
	case StateClosed:
		return ev == EventClosed
	default:
		return false
	}
}

// State returns the current state.
func (m *Machine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// TransitionCount returns the number of real state changes performed.
func (m *Machine) TransitionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitions
}

// IllegalCount returns the number of rejected illegal transitions.
func (m *Machine) IllegalCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.illegalTransitions
}

// DuplicateCount returns the number of duplicate events that were ignored.
func (m *Machine) DuplicateCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.duplicateEvents
}

// Trace returns a copy of the sequence of visited states.
func (m *Machine) Trace() []State {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]State, len(m.trace))
	copy(out, m.trace)
	return out
}
