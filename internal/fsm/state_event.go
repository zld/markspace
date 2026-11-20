// Package fsm implements the RTSP ingest control-plane state machine.
//
// The same machine is driven by both the UDP and the TCP-interleaved data
// paths: the gortsplib server callbacks translate ANNOUNCE / SETUP / RECORD /
// TEARDOWN protocol events into the events declared here.
package fsm

import "fmt"

// State is a control-plane state of an ingest session.
type State int

const (
	// StateCreated is the initial state of a session, before any request.
	StateCreated State = iota
	// StateAnnounced means ANNOUNCE has been accepted.
	StateAnnounced
	// StateReady means SETUP has been accepted and the transport is ready.
	StateReady
	// StateRecording means RECORD has been accepted and media may flow.
	StateRecording
	// StateClosing means TEARDOWN (or a fatal error) was observed.
	StateClosing
	// StateClosed is the terminal state.
	StateClosed
)

// String implements fmt.Stringer.
func (s State) String() string {
	switch s {
	case StateCreated:
		return "Created"
	case StateAnnounced:
		return "Announced"
	case StateReady:
		return "Ready"
	case StateRecording:
		return "Recording"
	case StateClosing:
		return "Closing"
	case StateClosed:
		return "Closed"
	default:
		return fmt.Sprintf("Unknown(%d)", int(s))
	}
}
