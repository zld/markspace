// Package signaling implements a local, in-process signaling channel with a
// strict state machine so connection-setup bugs are distinguishable from
// transport problems.
package signaling

import "fmt"

// ProtocolVersion is the only signaling version this tool speaks.
const ProtocolVersion = 1

// MessageType enumerates the signaling message kinds.
type MessageType string

const (
	TypeOffer     MessageType = "offer"
	TypeAnswer    MessageType = "answer"
	TypeCandidate MessageType = "candidate"
	TypeBye       MessageType = "bye"
)

// Message is a versioned signaling envelope. Payload carries the SDP string
// or a serialized ICE candidate depending on Type.
type Message struct {
	Version   int         `json:"version"`
	SessionID string      `json:"session_id"`
	Seq       uint64      `json:"seq"`
	Type      MessageType `json:"type"`
	Payload   string      `json:"payload"`
}

// FieldError describes a single invalid field of a message or parameter set.
type FieldError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func (e *FieldError) Error() string {
	return fmt.Sprintf("field %s: %s", e.Field, e.Reason)
}

// Validate checks the message envelope fields (not the state machine rules).
func (m Message) Validate() error {
	if m.Version != ProtocolVersion {
		return &FieldError{Field: "version", Reason: fmt.Sprintf("unsupported version %d, want %d", m.Version, ProtocolVersion)}
	}
	if m.SessionID == "" {
		return &FieldError{Field: "session_id", Reason: "must not be empty"}
	}
	switch m.Type {
	case TypeOffer, TypeAnswer, TypeCandidate, TypeBye:
	default:
		return &FieldError{Field: "type", Reason: fmt.Sprintf("unknown message type %q", string(m.Type))}
	}
	if (m.Type == TypeOffer || m.Type == TypeAnswer) && m.Payload == "" {
		return &FieldError{Field: "payload", Reason: "SDP payload must not be empty"}
	}
	return nil
}
