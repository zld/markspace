// Package errors defines the unified error model shared by every pipeline
// stage. Each error carries a stable machine-readable code, the logical
// subsystem that produced it and, when the failure is byte-addressable, the
// first locatable byte offset in the original bitstream.
package errors

import (
	stderrors "errors"
	"fmt"
)

// Code is a stable, machine-readable error code.
type Code string

const (
	// CodeOffsetOutOfRange means an index entry points outside the stream.
	CodeOffsetOutOfRange Code = "OFFSET_OUT_OF_RANGE"
	// CodeTruncatedUnit means a declared coding unit runs past end of stream.
	CodeTruncatedUnit Code = "TRUNCATED_UNIT"
	// CodeIndexMissing means a keyframe index entry is absent or duplicated.
	CodeIndexMissing Code = "INDEX_MISSING"
	// CodeInvalidEncoding means the bitstream bytes violate the codec syntax.
	CodeInvalidEncoding Code = "INVALID_ENCODING"
	// CodeClockBackward means a non-wrap timestamp regression was observed.
	CodeClockBackward Code = "CLOCK_BACKWARD"
	// CodeBoundaryConflict means representations share no common cut point.
	CodeBoundaryConflict Code = "BOUNDARY_CONFLICT"
	// CodeChecksum means a manifest or segment failed checksum validation.
	CodeChecksum Code = "CHECKSUM_MISMATCH"
	// CodeTxn means the write-ahead log or atomic commit is inconsistent.
	CodeTxn Code = "TXN_CORRUPT"
	// CodeStage means a pipeline stage failed an internal invariant.
	CodeStage Code = "STAGE_FAILED"
)

// Error is the single error type used across the project. It records the
// originating subsystem, a stable code, the first locatable byte offset in
// the original bitstream (when applicable, -1 otherwise) and a human message.
type Error struct {
	Subsystem string `json:"subsystem"`
	Code      Code   `json:"code"`
	// Offset is the first locatable byte in the ORIGINAL bitstream.
	// It is -1 when the failure has no byte address.
	Offset  int64  `json:"offset"`
	Message string `json:"message"`
	// Wrapped chains the underlying cause, if any.
	Wrapped error `json:"-"`
}

// New builds an Error without a byte offset.
func New(subsystem string, code Code, format string, args ...any) *Error {
	return &Error{
		Subsystem: subsystem,
		Code:      code,
		Offset:    -1,
		Message:   fmt.Sprintf(format, args...),
	}
}

// At builds an Error anchored at a specific original byte offset.
func At(subsystem string, code Code, offset int64, format string, args ...any) *Error {
	return &Error{
		Subsystem: subsystem,
		Code:      code,
		Offset:    offset,
		Message:   fmt.Sprintf(format, args...),
	}
}

// Wrap attaches a cause and returns the same error for chaining.
func (e *Error) Wrap(cause error) *Error {
	e.Wrapped = cause
	return e
}

// WithOffset overrides the locatable offset.
func (e *Error) WithOffset(offset int64) *Error {
	e.Offset = offset
	return e
}

func (e *Error) Error() string {
	if e.Offset >= 0 {
		return fmt.Sprintf("%s/%s at byte %d: %s", e.Subsystem, e.Code, e.Offset, e.Message)
	}
	return fmt.Sprintf("%s/%s: %s", e.Subsystem, e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Wrapped }

// AsError extracts a project *Error from any error value, returning nil
// when the chain does not contain one.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var pe *Error
	if ok := stderrors.As(err, &pe); ok {
		return pe
	}
	return nil
}
