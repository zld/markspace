// Package fault defines the unified error model shared by every subsystem.
// Errors carry a stable machine-readable code, the byte offset in the input
// that triggered them, the track number when relevant, and the DAG node that
// blocked execution when the failure is a dependency failure.
package fault

import (
	"errors"
	"fmt"
)

// Stable error codes. These strings are part of the module contract and must
// never change once released.
const (
	ErrTruncatedPacket = "ERR_TRUNCATED_PACKET"
	ErrCorruptBox      = "ERR_CORRUPT_BOX"
	ErrEmptyRange      = "ERR_EMPTY_RANGE"
	ErrDuplicateTrack  = "ERR_DUPLICATE_TRACK"
	ErrMissingSegment  = "ERR_MISSING_SEGMENT"
	ErrUnrepresentable = "ERR_UNREPRESENTABLE_TIME"
	ErrDependency      = "ERR_DEPENDENCY_BLOCKED"
	ErrInvalidArg      = "ERR_INVALID_ARGUMENT"
)

// NA is the offset value used when an error is not tied to a byte position.
const NA int64 = -1

// Error is the single error type flowing through the media kernel.
type Error struct {
	Code     string // one of the Err* constants
	Offset   int64  // byte offset in the offending input, NA when unknown
	Track    uint32 // track id; valid only when HasTrack is true
	HasTrack bool
	Node     string // DAG node id that is blocked; empty when not applicable
	Msg      string // human-readable detail
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%s: %s", e.Code, e.Msg)
	if e.Offset != NA {
		s += fmt.Sprintf(" (offset=%d)", e.Offset)
	}
	if e.HasTrack {
		s += fmt.Sprintf(" (track=%d)", e.Track)
	}
	if e.Node != "" {
		s += fmt.Sprintf(" (node=%s)", e.Node)
	}
	return s
}

// New builds an Error with the given code, input offset and message.
func New(code string, offset int64, format string, args ...any) *Error {
	return &Error{Code: code, Offset: offset, Msg: fmt.Sprintf(format, args...)}
}

// WithTrack annotates the error with a track id and returns it.
func (e *Error) WithTrack(track uint32) *Error {
	e.Track, e.HasTrack = track, true
	return e
}

// WithNode annotates the error with a blocking DAG node id and returns it.
func (e *Error) WithNode(node string) *Error {
	e.Node = node
	return e
}

// Is reports whether err (or anything it wraps) is a *fault.Error with the
// given stable code.
func Is(err error, code string) bool {
	var fe *Error
	if errors.As(err, &fe) {
		return fe.Code == code
	}
	return false
}

// OffsetOf extracts the byte offset carried by err, if any.
func OffsetOf(err error) (int64, bool) {
	var fe *Error
	if errors.As(err, &fe) && fe.Offset != NA {
		return fe.Offset, true
	}
	return 0, false
}
