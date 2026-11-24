package media

import (
	"errors"
	"fmt"
)

// Code is a stable, machine-readable error category.
type Code string

const (
	CodeOK                    Code = "OK"
	CodeInvalidArgument       Code = "INVALID_ARGUMENT"
	CodeInvalidTimebase       Code = "INVALID_TIMEBASE"
	CodeTimestampNegative     Code = "TIMESTAMP_NEGATIVE"
	CodeTimestampInvalid      Code = "TIMESTAMP_INVALID"
	CodeTimestampOverflow     Code = "TIMESTAMP_OVERFLOW"
	CodeTimestampNonMonotonic Code = "TIMESTAMP_NON_MONOTONIC"

	CodeRTPInvalid      Code = "RTP_INVALID"
	CodeSequenceGap     Code = "SEQUENCE_GAP"
	CodePacketDuplicate Code = "PACKET_DUPLICATE"

	CodeCodecInvalid    Code = "CODEC_INVALID"
	CodePacketEmpty     Code = "PACKET_EMPTY"
	CodePacketTruncated Code = "PACKET_TRUNCATED"
	CodePacketTooLong   Code = "PACKET_TOO_LONG"
	CodeUnsupported     Code = "UNSUPPORTED"

	CodeIndexMissing    Code = "INDEX_MISSING"
	CodeIndexMismatch   Code = "INDEX_MISMATCH"
	CodeKeyframeCorrupt Code = "KEYFRAME_CORRUPT"
	CodeMP4Invalid      Code = "MP4_INVALID"

	CodeDamageRegion Code = "DAMAGE_REGION"

	CodeQueueFull    Code = "QUEUE_FULL"
	CodeMemoryLimit  Code = "MEMORY_LIMIT"
	CodeCheckpoint   Code = "CHECKPOINT_INVALID"
	CodeHashMismatch Code = "HASH_MISMATCH"
	CodeInterrupted  Code = "INTERRUPTED"
)

// Error is the structured error model shared across subsystems.
type Error struct {
	Code      Code       `json:"code"`
	Module    string     `json:"module"`
	Message   string     `json:"message"`
	CauseText string     `json:"cause,omitempty"`
	Seq       *uint64    `json:"ext_seq,omitempty"`
	Offset    int64      `json:"offset,omitempty"`
	ByteRange *ByteRange `json:"byte_range,omitempty"`
	TimeRange *TimeRange `json:"time_range,omitempty"`
	Cause     error      `json:"-"`
}

// ByteRange is a half-open byte interval [Start, End).
type ByteRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// NewError creates a structured error.
func NewError(module string, code Code, message string, cause error) *Error {
	e := &Error{Module: module, Code: code, Message: message, Cause: cause}
	if cause != nil {
		e.CauseText = cause.Error()
	}
	return e
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	where := e.Module
	if e.Seq != nil {
		where += fmt.Sprintf("#%d", *e.Seq)
	}
	s := fmt.Sprintf("[%s/%s] %s", where, e.Code, e.Message)
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

// Unwrap exposes the wrapped cause.
func (e *Error) Unwrap() error { return e.Cause }

// WithSeq annotates an extended sequence number.
func (e *Error) WithSeq(seq uint64) *Error {
	e.Seq = &seq
	return e
}

// WithOffset annotates a byte offset.
func (e *Error) WithOffset(offset int64) *Error {
	e.Offset = offset
	if e.ByteRange == nil {
		e.ByteRange = &ByteRange{Start: offset, End: offset}
	}
	return e
}

// WithByteRange annotates a byte range.
func (e *Error) WithByteRange(start, end int64) *Error {
	e.ByteRange = &ByteRange{Start: start, End: end}
	e.Offset = start
	return e
}

// WithRange annotates a time range.
func (e *Error) WithRange(r *TimeRange) *Error {
	if r != nil {
		cp := *r
		e.TimeRange = &cp
	}
	return e
}

// AsError finds a *Error in err's wrap chain.
func AsError(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// HasCode reports whether err contains structured code c.
func HasCode(err error, c Code) bool {
	e, ok := AsError(err)
	return ok && e.Code == c
}
