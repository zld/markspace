// Package session implements the RTSP session state machine and the
// driver that runs the DESCRIBE/SETUP/PLAY/TEARDOWN method sequence.
//
// The state machine is transport-agnostic: both UDP and TCP-interleaved
// adapters drive the same Machine instance so the observed state
// transitions are identical regardless of the RTP transport.
package session

import (
	"fmt"
)

// State is a state of the RTSP session state machine.
type State string

// RTSP session states.
const (
	StateInit      State = "Init"
	StateOptions   State = "Options"
	StateDescribed State = "Described"
	StateSetup     State = "Setup"
	StatePlaying   State = "Playing"
	StateTeardown  State = "Teardown"
	StateClosed    State = "Closed"
	StateFailed    State = "Failed"
)

// Method is an RTSP method whose response drives a transition.
type Method string

// RTSP methods handled by the machine.
const (
	MethodOptions  Method = "OPTIONS"
	MethodDescribe Method = "DESCRIBE"
	MethodSetup    Method = "SETUP"
	MethodPlay     Method = "PLAY"
	MethodTeardown Method = "TEARDOWN"
)

// Event is the outcome of an RTSP method exchange as observed by the
// client. A successful response drives the normal transition; a failed
// response or transport error drives the machine into Failed.
type Event struct {
	Method Method
	// OK is true when the RTSP response indicates success (2xx).
	OK bool
	// TransportErr is set when the connection broke before a response
	// (or the response could not be read) instead of a protocol failure.
	TransportErr bool
}

// Transition records one observed legal state change.
type Transition struct {
	From   State
	To     State
	Method Method
}

// legalTransitions maps (current state, method) to the next state for
// successful method responses. Any other combination is illegal.
var legalTransitions = map[State]map[Method]State{
	StateInit: {
		MethodOptions: StateOptions,
	},
	StateOptions: {
		MethodDescribe: StateDescribed,
	},
	StateDescribed: {
		MethodSetup: StateSetup,
	},
	StateSetup: {
		MethodPlay: StatePlaying,
	},
	StatePlaying: {
		MethodTeardown: StateTeardown,
	},
	StateTeardown: {},
	StateClosed:   {},
	StateFailed:   {},
}

// Machine is the RTSP session state machine.
//
// Transitions are driven exclusively by RTSP method responses through
// Apply. An illegal method for the current state, an unsuccessful
// response or a transport error moves the machine into Failed while
// recording the stage (state) at which the failure happened.
type Machine struct {
	current      State
	failedStage  State
	failedMethod Method
	transitions  []Transition
	failed       bool
}

// NewMachine creates a machine in the Init state.
func NewMachine() *Machine {
	return &Machine{current: StateInit}
}

// Current returns the current state.
func (m *Machine) Current() State { return m.current }

// FailedStage returns the state the machine was in when it failed,
// or the empty string if it never failed.
func (m *Machine) FailedStage() State { return m.failedStage }

// FailedMethod returns the method whose event caused the failure, if any.
func (m *Machine) FailedMethod() Method { return m.failedMethod }

// Transitions returns a copy of every legal transition observed so far.
func (m *Machine) Transitions() []Transition {
	out := make([]Transition, len(m.transitions))
	copy(out, m.transitions)
	return out
}

// TransitionCount returns the number of legal state changes observed.
func (m *Machine) TransitionCount() int { return len(m.transitions) }

// Failed reports whether the machine entered the Failed state.
func (m *Machine) Failed() bool { return m.failed }

// Apply feeds one method event into the machine.
//
// On a legal, successful event the machine records a transition and
// returns nil. On an illegal order, non-2xx response or transport
// error the machine enters Failed, keeps the failing stage for later
// reporting and returns a descriptive error.
func (m *Machine) Apply(ev Event) error {
	if m.failed {
		return fmt.Errorf("session already failed at stage %s", m.failedStage)
	}

	stage := m.current
	if !ev.OK || ev.TransportErr {
		return m.fail(stage, ev.Method, fmt.Errorf(
			"method %s failed at state %s (ok=%v transport_err=%v)",
			ev.Method, stage, ev.OK, ev.TransportErr))
	}

	nexts, ok := legalTransitions[stage]
	if !ok {
		return m.fail(stage, ev.Method, fmt.Errorf(
			"no legal transition out of state %s (method %s)", stage, ev.Method))
	}
	next, ok := nexts[ev.Method]
	if !ok {
		return m.fail(stage, ev.Method, fmt.Errorf(
			"illegal method %s in state %s", ev.Method, stage))
	}

	m.transitions = append(m.transitions, Transition{
		From:   stage,
		To:     next,
		Method: ev.Method,
	})
	m.current = next
	return nil
}

// Close marks a fully torn-down session as Closed. It is legal only
// after the Teardown state; calling it anywhere else fails the machine.
func (m *Machine) Close() error {
	if m.failed {
		return fmt.Errorf("session already failed at stage %s", m.failedStage)
	}
	if m.current != StateTeardown {
		return m.fail(m.current, "", fmt.Errorf(
			"cannot close session from state %s: TEARDOWN not completed", m.current))
	}
	stage := m.current
	m.transitions = append(m.transitions, Transition{
		From: stage, To: StateClosed, Method: MethodTeardown,
	})
	m.current = StateClosed
	return nil
}

func (m *Machine) fail(stage State, method Method, err error) error {
	m.failed = true
	m.failedStage = stage
	m.failedMethod = method
	m.current = StateFailed
	return err
}
