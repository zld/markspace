// Package session implements the transport-independent RTSP session state
// machine used by both the UDP and TCP interleaved runners.
package session

import (
	"fmt"
	"sync"
)

// State is one stage of the client-side RTSP session lifecycle.
type State int

const (
	StateIdle State = iota
	StateConnected
	StateDescribed
	StateSetUp
	StatePlaying
	StateClosing
	StateClosed
)

// String renders states with stable uppercase names used in diagnostics.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "IDLE"
	case StateConnected:
		return "CONNECTED"
	case StateDescribed:
		return "DESCRIBED"
	case StateSetUp:
		return "SETUP"
	case StatePlaying:
		return "PLAYING"
	case StateClosing:
		return "CLOSING"
	case StateClosed:
		return "CLOSED"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", int(s))
	}
}

// Event drives a state transition.
type Event int

const (
	EvConnectOK Event = iota
	EvDescribeOK
	EvSetupOK
	EvPlayOK
	EvTeardown
	EvClosed
)

// String gives events stable names for error messages.
func (e Event) String() string {
	switch e {
	case EvConnectOK:
		return "CONNECT_OK"
	case EvDescribeOK:
		return "DESCRIBE_OK"
	case EvSetupOK:
		return "SETUP_OK"
	case EvPlayOK:
		return "PLAY_OK"
	case EvTeardown:
		return "TEARDOWN"
	case EvClosed:
		return "CLOSED"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", int(e))
	}
}

// Machine is a goroutine-safe finite state machine. The same transition table
// is used for UDP and TCP transports; transport-specific code only emits
// events.
type Machine struct {
	mu sync.Mutex

	current     State
	transitions int
	illegal     int
	// history records every accepted transition as "FROM->TO".
	history []string
}

// allowed encodes the single legal successor per event and state.
var allowed = map[State]map[Event]State{
	StateIdle: {
		EvConnectOK: StateConnected,
		EvTeardown:  StateClosing, // connect failed: straight to cleanup
	},
	StateConnected: {
		EvDescribeOK: StateDescribed,
		EvTeardown:   StateClosing,
	},
	StateDescribed: {
		EvSetupOK:  StateSetUp,
		EvTeardown: StateClosing,
	},
	StateSetUp: {
		EvPlayOK:   StatePlaying,
		EvTeardown: StateClosing,
	},
	StatePlaying: {
		EvTeardown: StateClosing,
	},
	StateClosing: {
		EvClosed: StateClosed,
	},
	StateClosed: {},
}

// NewMachine starts in StateIdle.
func NewMachine() *Machine {
	return &Machine{current: StateIdle}
}

// Handle applies an event. A legal event moves the machine and is counted as a
// transition; an illegal event increments the error counter and leaves the
// state unchanged.
func (m *Machine) Handle(ev Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	next, ok := allowed[m.current][ev]
	if !ok {
		m.illegal++
		return fmt.Errorf("illegal transition: event %s not allowed in state %s", ev, m.current)
	}

	from := m.current
	m.history = append(m.history, from.String()+"->"+next.String())
	m.current = next
	m.transitions++
	return nil
}

// State returns the current state.
func (m *Machine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// TransitionCount returns the number of accepted transitions.
func (m *Machine) TransitionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitions
}

// IllegalCount returns how many events were rejected.
func (m *Machine) IllegalCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.illegal
}

// History returns a copy of the accepted transition trace.
func (m *Machine) History() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.history))
	copy(out, m.history)
	return out
}

// Close applies the shared closing transitions (teardown, then closed).
// It is valid from every pre-closing state, so UDP and TCP runners use the
// exact same cleanup path. The returned error describes an illegal move.
func (m *Machine) Close() error {
	if err := m.Handle(EvTeardown); err != nil {
		return err
	}
	return m.Handle(EvClosed)
}
