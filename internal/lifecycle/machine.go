// Package lifecycle implements a reusable RTSP session state machine.
// Both UDP and TCP-interleaved clients drive the same machine, so
// transition accounting is identical across transports.
package lifecycle

import "fmt"

// State is a node in the RTSP session lifecycle.
type State int

const (
	StateIdle State = iota
	StateOptions
	StateDescribe
	StateSetup
	StatePlay
	StateClosed
	StateEnded
)

// String returns a human readable state name.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "IDLE"
	case StateOptions:
		return "OPTIONS"
	case StateDescribe:
		return "DESCRIBE"
	case StateSetup:
		return "SETUP"
	case StatePlay:
		return "PLAY"
	case StateClosed:
		return "CLOSED"
	case StateEnded:
		return "ENDED"
	}
	return "UNKNOWN"
}

// Event is a lifecycle transition trigger.
type Event int

const (
	EventOptions Event = iota
	EventDescribe
	EventSetup
	EventPlay
	EventClose
	EventEnd
)

// String returns a human readable event name.
func (e Event) String() string {
	switch e {
	case EventOptions:
		return "OPTIONS"
	case EventDescribe:
		return "DESCRIBE"
	case EventSetup:
		return "SETUP"
	case EventPlay:
		return "PLAY"
	case EventClose:
		return "CLOSE"
	case EventEnd:
		return "END"
	}
	return "UNKNOWN"
}

// transition describes one recorded state migration.
type transition struct {
	from  State
	event Event
	to    State
}

// Machine tracks the RTSP lifecycle. Illegal transitions are rejected
// and leave the current state untouched.
type Machine struct {
	state  State
	hist   []transition
	failed int
}

// table of legal transitions: table[from][event] = to.
var table = map[State]map[Event]State{
	StateIdle:     {EventOptions: StateOptions},
	StateOptions:  {EventDescribe: StateDescribe},
	StateDescribe: {EventSetup: StateSetup},
	StateSetup:    {EventPlay: StatePlay},
	StatePlay:     {EventClose: StateClosed},
	StateClosed:   {EventEnd: StateEnded},
}

// New returns a machine in StateIdle.
func New() *Machine {
	return &Machine{state: StateIdle}
}

// Transition applies ev. On an illegal transition it returns an error
// and the state and transition count are left unchanged.
func (m *Machine) Transition(ev Event) error {
	nexts, ok := table[m.state]
	if !ok {
		m.failed++
		return fmt.Errorf("lifecycle: no transitions from state %s (event %s)", m.state, ev)
	}
	next, ok := nexts[ev]
	if !ok {
		m.failed++
		return fmt.Errorf("lifecycle: illegal transition %s -> event %s", m.state, ev)
	}
	m.hist = append(m.hist, transition{from: m.state, event: ev, to: next})
	m.state = next
	return nil
}

// State returns the current state.
func (m *Machine) State() State {
	return m.state
}

// TransitionCount returns the number of successful transitions.
func (m *Machine) TransitionCount() int {
	return len(m.hist)
}

// RejectedCount returns the number of rejected (illegal) transitions.
func (m *Machine) RejectedCount() int {
	return m.failed
}

// History returns the recorded transitions as "FROM -EVENT-> TO" strings.
func (m *Machine) History() []string {
	out := make([]string, 0, len(m.hist))
	for _, t := range m.hist {
		out = append(out, fmt.Sprintf("%s -%s-> %s", t.from, t.event, t.to))
	}
	return out
}
