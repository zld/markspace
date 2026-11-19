// Package session defines the RTSP session state machine and its
// transition rules. The same machine drives both the interleaved-TCP
// and the UDP transport adapters.
package session

import "fmt"

// State is one state of an RTSP validation session.
type State string

const (
	// StateIdle is the initial state before any network activity.
	StateIdle State = "Idle"
	// StateConnecting covers TCP connect and the OPTIONS exchange.
	StateConnecting State = "Connecting"
	// StateDescribed means DESCRIBE completed successfully.
	StateDescribed State = "Described"
	// StateSetup means SETUP completed and the transport is ready.
	StateSetup State = "Setup"
	// StatePlaying means PLAY completed and RTP is flowing.
	StatePlaying State = "Playing"
	// StateStopping covers the TEARDOWN exchange.
	StateStopping State = "Stopping"
	// StateClosed is the terminal state after a clean shutdown.
	StateClosed State = "Closed"
	// StateFailed is reached when negotiation, transport or
	// processing fails. From there the runner still releases
	// every listener, connection and UDP socket.
	StateFailed State = "Failed"
)

// Event is a state-machine input, one per RTSP phase or failure path.
type Event string

const (
	EventConnect     Event = "Connect"
	EventDescribeOK  Event = "DescribeOK"
	EventSetupOK     Event = "SetupOK"
	EventPlayOK      Event = "PlayOK"
	EventStop        Event = "Stop"
	EventClosed      Event = "Closed"
	EventFail        Event = "Fail"
	EventRetryClosed Event = "RetryClosed"
)

// allowedTransitions is the single rule table shared by both
// transport modes. An event not present for the current state is an
// illegal transition.
var allowedTransitions = map[State]map[Event]State{
	StateIdle: {
		EventConnect: StateConnecting,
		EventFail:    StateFailed,
	},
	StateConnecting: {
		EventDescribeOK: StateDescribed,
		EventFail:       StateFailed,
	},
	StateDescribed: {
		EventSetupOK: StateSetup,
		EventFail:    StateFailed,
	},
	StateSetup: {
		EventPlayOK: StatePlaying,
		EventFail:   StateFailed,
	},
	StatePlaying: {
		EventStop: StateStopping,
		EventFail: StateFailed,
	},
	StateStopping: {
		EventClosed: StateClosed,
		EventFail:   StateFailed,
	},
	StateFailed: {
		// Resource cleanup always ends in Closed.
		EventRetryClosed: StateClosed,
	},
	StateClosed: {},
}

// Machine is a concurrency-safe RTSP state machine. Every legal
// transition increments TransitionCount; rejected transitions are
// reported as locatable errors without changing the state.
type Machine struct {
	state            State
	transitionCount  int
	illegalRejections int
	history          []transitionRecord
}

type transitionRecord struct {
	from  State
	event Event
	to    State
}

// NewMachine returns a machine in StateIdle.
func NewMachine() *Machine {
	return &Machine{state: StateIdle, history: make([]transitionRecord, 0, 16)}
}

// State returns the current state.
func (m *Machine) State() State { return m.state }

// TransitionCount is the number of legal transitions performed.
func (m *Machine) TransitionCount() int { return m.transitionCount }

// IllegalRejections is the number of rejected transition attempts.
func (m *Machine) IllegalRejections() int { return m.illegalRejections }

// History returns a copy of the recorded (from, event, to) entries.
func (m *Machine) History() []Transition {
	out := make([]Transition, len(m.history))
	for i, r := range m.history {
		out[i] = Transition{From: r.from, Event: r.event, To: r.to}
	}
	return out
}

// Transition describes one recorded state transition.
type Transition struct {
	From  State
	Event Event
	To    State
}

// IllegalTransitionError locates a rejected transition.
type IllegalTransitionError struct {
	From  State
	Event Event
}

func (e *IllegalTransitionError) Error() string {
	return fmt.Sprintf("illegal state transition: event %s not allowed in state %s",
		e.Event, e.From)
}

// Transition applies event to the current state. A legal event moves
// the machine and is counted; an illegal event increments the
// rejection counter and returns an *IllegalTransitionError.
func (m *Machine) Transition(event Event) error {
	next, ok := allowedTransitions[m.state][event]
	if !ok {
		m.illegalRejections++
		return &IllegalTransitionError{From: m.state, Event: event}
	}
	from := m.state
	m.state = next
	m.transitionCount++
	m.history = append(m.history, transitionRecord{from: from, event: event, to: next})
	return nil
}

// IsTerminal reports whether the machine has reached a terminal state.
func (m *Machine) IsTerminal() bool {
	return m.state == StateClosed
}
