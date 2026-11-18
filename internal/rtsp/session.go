// Package rtsp contains the project-owned RTSP session state machine and the
// UDP / RTP-over-TCP transport adapters that both drive the same machine.
package rtsp

import (
	"fmt"
	"sync"
)

// State is one RTSP session state.
type State int

const (
	StateInit State = iota
	StateOptions
	StateDescribe
	StateSetup
	StatePlay
	StateTeardown
)

// String renders the state for reports and error messages.
func (s State) String() string {
	switch s {
	case StateInit:
		return "INIT"
	case StateOptions:
		return "OPTIONS"
	case StateDescribe:
		return "DESCRIBE"
	case StateSetup:
		return "SETUP"
	case StatePlay:
		return "PLAY"
	case StateTeardown:
		return "TEARDOWN"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", int(s))
	}
}

// Method is an RTSP request handled by the state machine.
type Method int

const (
	MethodOptions Method = iota
	MethodDescribe
	MethodSetup
	MethodPlay
	MethodTeardown
)

// String renders the method name.
func (m Method) String() string {
	switch m {
	case MethodOptions:
		return "OPTIONS"
	case MethodDescribe:
		return "DESCRIBE"
	case MethodSetup:
		return "SETUP"
	case MethodPlay:
		return "PLAY"
	case MethodTeardown:
		return "TEARDOWN"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", int(m))
	}
}

// Transition records one accepted state move. Invalid attempts are not
// recorded.
type Transition struct {
	From State
	To   State
}

// allowed defines the legal RTSP method in every state. SETUP is repeatable
// (multiple tracks); OPTIONS may re-occur before SETUP; TEARDOWN is accepted
// from any active state.
var allowed = map[State]map[Method]State{
	StateInit: {
		MethodOptions: StateOptions,
	},
	StateOptions: {
		MethodOptions:  StateOptions,
		MethodDescribe: StateDescribe,
	},
	StateDescribe: {
		MethodSetup:   StateSetup,
		MethodOptions: StateOptions,
	},
	StateSetup: {
		MethodSetup:    StateSetup,
		MethodPlay:     StatePlay,
		MethodOptions:  StateOptions,
		MethodTeardown: StateTeardown,
	},
	StatePlay: {
		MethodTeardown: StateTeardown,
	},
	StateTeardown: {},
}

// ErrInvalidTransition is returned for a method that is not legal in the
// current state. The move is not counted.
type ErrInvalidTransition struct {
	State  State
	Method Method
}

func (e *ErrInvalidTransition) Error() string {
	return fmt.Sprintf("rtsp: illegal %s in state %s", e.Method, e.State)
}

// Session is the single state machine reused by UDP and TCP adapters.
type Session struct {
	mu sync.Mutex

	state       State
	transitions []Transition
	// rejected counts invalid attempts; they never appear in transitions.
	rejected int
}

// NewSession starts a machine in INIT.
func NewSession() *Session {
	return &Session{state: StateInit}
}

// State returns the current state.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// TransitionCount returns the number of accepted transitions.
func (s *Session) TransitionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.transitions)
}

// Transitions returns a copy of the accepted transition log.
func (s *Session) Transitions() []Transition {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Transition, len(s.transitions))
	copy(out, s.transitions)
	return out
}

// RejectedCount returns how many illegal attempts were refused.
func (s *Session) RejectedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rejected
}

// Apply attempts one method. On success it records the transition; on an
// illegal order it returns *ErrInvalidTransition without mutating state.
func (s *Session) Apply(method Method) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next, ok := allowed[s.state][method]
	if !ok {
		s.rejected++
		return &ErrInvalidTransition{State: s.state, Method: method}
	}
	from := s.state
	s.state = next
	// Self loops (repeat SETUP, repeat OPTIONS) are still valid moves and are
	// recorded because the request genuinely succeeded on the wire.
	s.transitions = append(s.transitions, Transition{From: from, To: next})
	return nil
}

// Reset returns the machine to INIT and clears the transition log so a
// disconnect cannot pollute the next run.
func (s *Session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = StateInit
	s.transitions = nil
	s.rejected = 0
}
