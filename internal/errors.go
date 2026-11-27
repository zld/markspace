// Package rtpreplay implements offline regression analysis for timestamped
// audio RTP traces. It reconstructs a unified media timeline from RTP and MP4
// inputs, computes transport metrics, and produces a reproducible manifest.
package rtpreplay

import (
	"errors"
	"fmt"
)

// ErrorCode is a stable, machine-readable classification for processing
// failures. Codes are part of the report schema and must not be renumbered.
type ErrorCode string

const (
	CodeInvalidInput  ErrorCode = "invalid_input"
	CodeInvalidConfig ErrorCode = "invalid_config"
	CodeRTPParse      ErrorCode = "rtp_parse"
	CodeUnknownCodec  ErrorCode = "unknown_codec"
	CodeDuration      ErrorCode = "duration_unknown"
	CodeTimeOverflow  ErrorCode = "time_overflow"
	CodeTimestampJump ErrorCode = "rtp_timestamp_jump"
	CodeMP4           ErrorCode = "mp4_parse"
	CodeAACHeader     ErrorCode = "aac_au_header"
	CodeInjected      ErrorCode = "injected_fault"
	CodeNodeBlocked   ErrorCode = "node_blocked"
	CodeNoData        ErrorCode = "no_media_data"
)

// Error is the structured error model shared by the trace, regression, and
// scheduling subsystems. Track and packet sequence are attached whenever the
// failure can be tied to a specific RTP packet, as required for offline
// triage of weak-network traces.
type Error struct {
	Code      ErrorCode
	Op        string
	Track     string
	PacketSeq *uint32
	Message   string
	Retryable bool
	Cause     error
}

func (e *Error) Error() string {
	msg := string(e.Code)
	if e.Op != "" {
		msg = e.Op + ": " + msg
	}
	if e.Track != "" {
		msg += " track=" + e.Track
	}
	if e.PacketSeq != nil {
		msg += fmt.Sprintf(" seq=%d", *e.PacketSeq)
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Cause }

// NewError constructs a structured error with a stable code.
func NewError(code ErrorCode, op, message string) *Error {
	return &Error{Code: code, Op: op, Message: message}
}

// WithTrack attaches a track identifier.
func (e *Error) WithTrack(track string) *Error {
	e.Track = track
	return e
}

// WithPacket attaches the RTP sequence number of the offending packet.
func (e *Error) WithPacket(seq uint16) *Error {
	v := uint32(seq)
	e.PacketSeq = &v
	return e
}

// WithRetryable marks whether a scheduler may retry the failing operation.
func (e *Error) WithRetryable(v bool) *Error {
	e.Retryable = v
	return e
}

// WithCause attaches an underlying error.
func (e *Error) WithCause(err error) *Error {
	e.Cause = err
	return e
}

// AsError normalizes any error into *Error. Untyped errors are treated as
// non-retryable input failures so a scheduler never retries a bug silently.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var se *Error
	if errors.As(err, &se) {
		return se
	}
	return &Error{Code: CodeInvalidInput, Op: "internal", Message: err.Error(), Cause: err}
}

// IsRetryable reports whether err is a structured, retryable failure.
func IsRetryable(err error) bool {
	var se *Error
	if errors.As(err, &se) {
		return se.Retryable
	}
	return false
}
