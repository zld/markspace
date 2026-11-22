package media

import (
	"errors"
	"fmt"
)

// Code is a stable, machine-readable error identifier shared by every stage.
type Code string

const (
	CodeMalformed          Code = "malformed_input"      // container/packet structurally invalid
	CodeMissingTable       Code = "missing_sample_table" // required MP4 sample table absent
	CodeOffsetOutOfRange   Code = "offset_out_of_range"  // chunk/sample offset outside file
	CodeBadEditSegment     Code = "bad_edit_segment"     // edit list references invalid media time
	CodeUnsupportedVersion Code = "unsupported_version"  // adapter/box version not negotiated
	CodeIncompatible       Code = "incompatible_adapter" // capability negotiation refused
	CodeOverflow           Code = "integer_overflow"     // declared size/duration overflows
	CodeLimitExceeded      Code = "limit_exceeded"       // kernel resource limit violated
	CodeBadPacket          Code = "bad_packet"           // unparsable or corrupt RTP packet
	CodeLatePacket         Code = "late_packet"          // packet arrived after commit boundary
	CodeDuplicatePacket    Code = "duplicate_packet"     // packet already committed
	CodeClockReset         Code = "clock_reset"          // sender clock discontinuity
	CodeNegativeDuration   Code = "negative_duration"    // frame/interval end <= start
	CodePTSRollback        Code = "pts_rollback"         // PTS moved backwards on a track
	CodeIncompleteFrame    Code = "incomplete_frame"     // frame declared but payload missing
	CodeCheckpointMismatch Code = "checkpoint_mismatch"  // resumed state disagrees with checkpoint
)

// rejectable codes mean the input itself is invalid and must be refused with
// process exit code 2 rather than repaired at runtime.
var rejectable = map[Code]bool{
	CodeMalformed: true, CodeMissingTable: true, CodeOffsetOutOfRange: true,
	CodeBadEditSegment: true, CodeUnsupportedVersion: true, CodeIncompatible: true,
	CodeOverflow: true, CodeLimitExceeded: true,
}

// Error is the single error model for the kernel. It carries enough location
// information to replay the failure against the original byte stream.
type Error struct {
	Code       Code      `json:"code"`
	TrackID    uint32    `json:"track_id"`
	When       *Interval `json:"when,omitempty"`
	ByteOffset int64     `json:"byte_offset"` // -1 when unknown
	Detail     string    `json:"detail"`
	cause      error
}

// NewError builds a located error. Pass -1 for byteOffset when unknown.
func NewError(code Code, track uint32, when *Interval, byteOffset int64, detail string) *Error {
	return &Error{Code: code, TrackID: track, When: when, ByteOffset: byteOffset, Detail: detail}
}

// Errorf is the formatted variant of NewError.
func Errorf(code Code, track uint32, when *Interval, byteOffset int64, format string, args ...interface{}) *Error {
	return NewError(code, track, when, byteOffset, fmt.Sprintf(format, args...))
}

// Wrap attaches a causal error without losing the kernel location.
func (e *Error) Wrap(cause error) *Error {
	e.cause = cause
	return e
}

func (e *Error) Error() string {
	s := fmt.Sprintf("media error %s: %s", e.Code, e.Detail)
	if e.TrackID != 0 {
		s += fmt.Sprintf(" (track=%d)", e.TrackID)
	}
	if e.When != nil {
		s += fmt.Sprintf(" (interval=[%d,%d) ns)", e.When.Start, e.When.End)
	}
	if e.ByteOffset >= 0 {
		s += fmt.Sprintf(" (offset=%d)", e.ByteOffset)
	}
	if e.cause != nil {
		s += ": " + e.cause.Error()
	}
	return s
}

// Unwrap exposes the wrapped cause to errors.Is/errors.As.
func (e *Error) Unwrap() error { return e.cause }

// Rejectable reports whether the error invalidates the input (exit code 2).
func (e *Error) Rejectable() bool { return rejectable[e.Code] }

// AsError extracts a *media.Error from any wrapped error chain.
func AsError(err error) (*Error, bool) {
	var me *Error
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// RejectableError reports whether err is a kernel error that invalidates input.
func RejectableError(err error) bool {
	if me, ok := AsError(err); ok {
		return me.Rejectable()
	}
	return false
}
