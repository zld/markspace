// Package mediaerr defines the structured error model shared by the
// parsing, timeline, audio and muxing subsystems.
package mediaerr

import (
	"errors"
	"fmt"
)

// Code classifies a recoverable or fatal media processing failure.
type Code string

const (
	// CodeBadPacket marks an RTP packet whose header failed validation.
	CodeBadPacket Code = "bad_packet"
	// CodeTruncated marks a payload or record that ended before its declared size.
	CodeTruncated Code = "truncated"
	// CodeCorrupt marks a payload that failed its integrity check.
	CodeCorrupt Code = "corrupt"
	// CodeLoss marks a detected sequence-number gap.
	CodeLoss Code = "loss"
	// CodeUnrecoverable marks a media interval with no usable samples.
	CodeUnrecoverable Code = "unrecoverable"
	// CodeParam marks invalid command line or configuration input.
	CodeParam Code = "param"
	// CodeVerify marks a failed post-processing verification.
	CodeVerify Code = "verify"
	// CodeIO marks a local filesystem failure.
	CodeIO Code = "io"
)

// Interval is a half-open media range expressed in samples at a stated rate.
type Interval struct {
	Start int64
	End   int64
}

// Error carries machine-readable context for a media failure. Seq and Offset
// are optional; HasSeq and HasOffset distinguish a real zero value.
type Error struct {
	Code      Code
	Seq       uint16
	HasSeq    bool
	Offset    int64
	HasOffset bool
	Interval  Interval
	HasRange  bool
	Message   string
	Err       error
}

// New creates an Error with a formatted message.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// At creates an Error at a byte offset (and optionally an RTP sequence number).
func At(code Code, offset int64, seq uint16, hasSeq bool, format string, args ...any) *Error {
	return &Error{
		Code:      code,
		Offset:    offset,
		HasOffset: true,
		Seq:       seq,
		HasSeq:    hasSeq,
		Message:   fmt.Sprintf(format, args...),
	}
}

// WithRange attaches an affected sample interval.
func (e *Error) WithRange(start, end int64) *Error {
	e.Interval = Interval{Start: start, End: end}
	e.HasRange = true
	return e
}

// WithCause attaches the underlying error.
func (e *Error) WithCause(err error) *Error {
	e.Err = err
	return e
}

func (e *Error) Error() string {
	s := string(e.Code)
	if e.HasOffset {
		s += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.HasSeq {
		s += fmt.Sprintf(" seq=%d", e.Seq)
	}
	if e.HasRange {
		s += fmt.Sprintf(" range=[%d,%d)", e.Interval.Start, e.Interval.End)
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap exposes the wrapped cause.
func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the media error code of err, or "" when err is not a media error.
func CodeOf(err error) Code {
	var me *Error
	if errors.As(err, &me) {
		return me.Code
	}
	return ""
}

// Is reports whether err is a media error with the given code.
func Is(err error, code Code) bool { return CodeOf(err) == code }
