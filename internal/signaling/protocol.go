// Package signaling implements the loopback-only HTTP signaling server, the
// wire protocol validation and the session state machine used by the two
// in-process peers during an experiment.
package signaling

import (
	"errors"
	"fmt"
	"strings"
)

// Peer identifiers inside a session.
const (
	PeerA = "A"
	PeerB = "B"
)

// Message types exchanged over the signaling channel.
const (
	MsgOffer     = "offer"
	MsgAnswer    = "answer"
	MsgCandidate = "candidate"
	MsgBye       = "bye"
)

// ProtocolError marks payloads that are structurally invalid (bad SDP,
// malformed candidate, unknown peer). The CLI maps these to exit code 2.
type ProtocolError struct{ Reason string }

func (e *ProtocolError) Error() string { return "protocol error: " + e.Reason }

// StaleError marks messages that fail the monotonic sequence-number check.
// They are rejected and counted, never applied to the session.
type StaleError struct {
	Peer     string
	Got      uint64
	Expected uint64
}

func (e *StaleError) Error() string {
	return fmt.Sprintf("stale message from peer %s: seq=%d expected>=%d", e.Peer, e.Got, e.Expected)
}

// Message is the single wire envelope used for SDP, trickle ICE and bye.
type Message struct {
	Type      string `json:"type"`
	From      string `json:"from"`
	To        string `json:"to"`
	Seq       uint64 `json:"seq"`
	SDP       string `json:"sdp,omitempty"`
	Candidate string `json:"candidate,omitempty"`
	SDPMid    string `json:"sdp_mid,omitempty"`
}

// ValidatePayload performs content validation independent of session state.
func (m *Message) ValidatePayload() error {
	if m.From != PeerA && m.From != PeerB {
		return &ProtocolError{Reason: "unknown sender: " + m.From}
	}
	if m.To != PeerA && m.To != PeerB {
		return &ProtocolError{Reason: "unknown recipient: " + m.To}
	}
	if m.From == m.To {
		return &ProtocolError{Reason: "sender and recipient must differ"}
	}
	switch m.Type {
	case MsgOffer, MsgAnswer:
		if err := ValidateSDP(m.SDP, m.Type); err != nil {
			return err
		}
	case MsgCandidate:
		if err := ValidateCandidateLine(m.Candidate); err != nil {
			return err
		}
	case MsgBye:
		// no payload required
	default:
		return &ProtocolError{Reason: "unknown message type: " + m.Type}
	}
	return nil
}

// ValidateSDP checks the mandatory SDP shape used in the experiment.
func ValidateSDP(sdp string, kind string) error {
	if strings.TrimSpace(sdp) == "" {
		return &ProtocolError{Reason: "empty SDP"}
	}
	if !strings.HasPrefix(sdp, "v=0") {
		return &ProtocolError{Reason: "SDP missing v=0 version line"}
	}
	if !strings.Contains(sdp, "\r\no=") && !strings.Contains(sdp, "\no=") {
		return &ProtocolError{Reason: "SDP missing origin line"}
	}
	if !strings.Contains(sdp, "m=audio") {
		return &ProtocolError{Reason: "SDP missing m=audio media section"}
	}
	switch kind {
	case MsgOffer:
		if !strings.Contains(sdp, "a=ice-ufrag:") || !strings.Contains(sdp, "a=ice-pwd:") {
			return &ProtocolError{Reason: "offer missing ICE credentials"}
		}
	case MsgAnswer:
		if !strings.Contains(sdp, "a=ice-ufrag:") || !strings.Contains(sdp, "a=ice-pwd:") {
			return &ProtocolError{Reason: "answer missing ICE credentials"}
		}
	default:
		return &ProtocolError{Reason: "invalid SDP kind: " + kind}
	}
	return nil
}

// ValidateCandidateLine validates a trickle candidate attribute line, e.g.
// "candidate:1 1 udp 2130706431 127.0.0.1 50000 typ host".
func ValidateCandidateLine(line string) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return &ProtocolError{Reason: "empty candidate line"}
	}
	if !strings.HasPrefix(line, "candidate:") {
		return &ProtocolError{Reason: "candidate line must start with candidate:"}
	}
	fields := strings.Fields(line)
	if len(fields) < 8 {
		return &ProtocolError{Reason: "candidate line has too few fields"}
	}
	if fields[2] != "udp" {
		return &ProtocolError{Reason: "only udp candidates are permitted"}
	}
	if fields[6] != "typ" {
		return &ProtocolError{Reason: "candidate line missing typ token"}
	}
	switch fields[7] {
	case "host", "srflx", "relay", "prflx":
	default:
		return &ProtocolError{Reason: "invalid candidate type: " + fields[8]}
	}
	if strings.Contains(line, ".local") {
		return &ProtocolError{Reason: "mDNS (.local) candidates are rejected"}
	}
	return nil
}

// ErrSessionClosed is returned when a message arrives after teardown.
var ErrSessionClosed = errors.New("session closed")
