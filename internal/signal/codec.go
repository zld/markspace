// Package signal implements the local in-process signaling channel:
// a versioned JSON envelope codec and a per-endpoint state machine.
package signal

import (
	"encoding/json"
	"fmt"
)

// Version is the only envelope version this build accepts.
const Version = "1"

// MaxMessageSize bounds a single encoded signaling message.
const MaxMessageSize = 64 * 1024

// MessageType identifies the payload carried by an Envelope.
type MessageType string

const (
	TypeOffer  MessageType = "offer"
	TypeAnswer MessageType = "answer"
	TypeICE    MessageType = "ice"
	TypeClose  MessageType = "close"
)

// Envelope is the wire form of every signaling message.
type Envelope struct {
	Version string          `json:"version"`
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// SDPPayload carries a session description between peers.
type SDPPayload struct {
	SDP  string `json:"sdp"`
	Type string `json:"type"` // "offer" or "answer"
}

// ICEPayload carries a single ICE candidate (empty Candidate = end-of-candidates).
type ICEPayload struct {
	Candidate     string `json:"candidate"`
	SDPMid        string `json:"sdp_mid,omitempty"`
	SDPMLineIndex uint16 `json:"sdp_mline_index,omitempty"`
}

// Encode marshals payload into a versioned envelope and enforces the size cap.
func Encode(msgType MessageType, payload any) ([]byte, error) {
	switch msgType {
	case TypeOffer, TypeAnswer, TypeICE, TypeClose:
	default:
		return nil, fmt.Errorf("signal: unknown message type %q", msgType)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("signal: marshal payload: %w", err)
	}
	env := Envelope{Version: Version, Type: msgType, Payload: raw}
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("signal: marshal envelope: %w", err)
	}
	if len(data) > MaxMessageSize {
		return nil, fmt.Errorf("signal: message too large: %d > %d bytes", len(data), MaxMessageSize)
	}
	return data, nil
}

// Decode parses and validates an envelope. It rejects oversized input,
// truncated/invalid JSON, unknown versions and unknown message types.
func Decode(data []byte) (*Envelope, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("signal: empty message")
	}
	if len(data) > MaxMessageSize {
		return nil, fmt.Errorf("signal: message too large: %d > %d bytes", len(data), MaxMessageSize)
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("signal: invalid or truncated message: %w", err)
	}
	if env.Version != Version {
		return nil, fmt.Errorf("signal: unsupported version %q (want %q)", env.Version, Version)
	}
	switch env.Type {
	case TypeOffer, TypeAnswer, TypeICE, TypeClose:
	default:
		return nil, fmt.Errorf("signal: unknown message type %q", env.Type)
	}
	if env.Type != TypeClose && len(env.Payload) == 0 {
		return nil, fmt.Errorf("signal: %s message missing payload", env.Type)
	}
	return &env, nil
}

// DecodePayload unmarshals the envelope payload into v.
func DecodePayload(env *Envelope, v any) error {
	if err := json.Unmarshal(env.Payload, v); err != nil {
		return fmt.Errorf("signal: decode %s payload: %w", env.Type, err)
	}
	return nil
}
