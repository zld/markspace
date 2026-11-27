// Package serr defines the structured error type shared by the EBML parser,
// the Matroska reader and the clock/event mapping layer. Every error that can
// be attributed to a byte offset or an element path is reported through Error
// so that callers can surface machine readable codes instead of free text.
package serr

import (
	"errors"
	"fmt"
	"strings"
)

// Code is a stable, machine readable error identifier.
type Code string

const (
	// CodeIllegalVINT reports a variable length integer that cannot be decoded.
	CodeIllegalVINT Code = "ILLEGAL_VINT"
	// CodeTruncatedHeader reports an element header cut off by EOF or a parent boundary.
	CodeTruncatedHeader Code = "TRUNCATED_HEADER"
	// CodeLengthOutOfBounds reports an element whose declared size crosses its parent.
	CodeLengthOutOfBounds Code = "LENGTH_OUT_OF_BOUNDS"
	// CodeUnknownElement reports an element whose ID is not a valid Matroska EBML ID.
	CodeUnknownElement Code = "UNKNOWN_ELEMENT"
	// CodeTruncatedBlock reports a block whose payload is shorter than its header claims.
	CodeTruncatedBlock Code = "TRUNCATED_BLOCK"
	// CodeParentBoundary reports an unknown-length element closed at an illegal boundary.
	CodeParentBoundary Code = "PARENT_BOUNDARY_CONFLICT"
	// CodeClockOverflow reports a time conversion that does not fit in int64 nanoseconds.
	CodeClockOverflow Code = "CLOCK_OVERFLOW"
	// CodeNoAnchor reports an event whose source has no mapping anchor.
	CodeNoAnchor Code = "NO_ANCHOR"
	// CodeMetadataConflict reports contradictory track metadata; bytes are never rewritten.
	CodeMetadataConflict Code = "METADATA_CONFLICT"
	// CodeCheckpointCorrupt reports an unreadable or tampered checkpoint.
	CodeCheckpointCorrupt Code = "CHECKPOINT_CORRUPT"
	// CodeRangeOutOfBounds reports a read range crossing the indexed media span.
	CodeRangeOutOfBounds Code = "RANGE_OUT_OF_BOUNDS"
	// CodeIO wraps local input/output failures.
	CodeIO Code = "IO_ERROR"
	// CodeInvalidArgs wraps invalid command line or function arguments.
	CodeInvalidArgs Code = "INVALID_ARGS"
)

// Error is the structured error type shared across subsystems.
type Error struct {
	Code   Code     // stable identifier
	Offset int64    // input byte offset, -1 when not applicable
	Path   []string // element path from the document root
	Msg    string   // human readable detail
}

// New builds an Error. Offset is left at -1 (not applicable).
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Offset: -1, Path: nil, Msg: fmt.Sprintf(format, args...)}
}

// At annotates the error with a byte offset and returns it.
func (e *Error) At(offset int64) *Error { e.Offset = offset; return e }

// WithPath annotates the error with an element path and returns it.
func (e *Error) WithPath(path []string) *Error {
	if len(path) > 0 {
		e.Path = append([]string(nil), path...)
	}
	return e
}

// Error implements the error interface.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	if e.Offset >= 0 {
		fmt.Fprintf(&b, " @offset=%d", e.Offset)
	}
	if len(e.Path) > 0 {
		b.WriteString(" path=")
		b.WriteString(strings.Join(e.Path, "/"))
	}
	if e.Msg != "" {
		b.WriteString(": ")
		b.WriteString(e.Msg)
	}
	return b.String()
}

// Is supports errors.Is by comparing codes when both operands are *Error.
func (e *Error) Is(target error) bool {
	var t *Error
	if errors.As(target, &t) {
		return e.Code == t.Code
	}
	return false
}

// As returns the first *Error in err's chain.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// CodeOf returns err's code, or CodeIO for ordinary (non structured) errors.
func CodeOf(err error) Code {
	if e, ok := As(err); ok {
		return e.Code
	}
	return CodeIO
}

// OffsetOf returns err's byte offset, or -1 when not applicable.
func OffsetOf(err error) int64 {
	if e, ok := As(err); ok {
		return e.Offset
	}
	return -1
}
