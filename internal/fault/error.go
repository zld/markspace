// Package fault provides the single error model used across avpipe.
// Parse and scan errors retain the first unreadable byte offset so callers
// can quarantine exactly the affected sample, segment, or representation.
package fault

import (
	"errors"
	"fmt"
)

// Kind classifies a failure for process exit codes and reporting.
type Kind string

const (
	KindArgument     Kind = "argument"
	KindTruncated    Kind = "truncated"
	KindOutOfBounds  Kind = "out_of_bounds"
	KindInvalid      Kind = "invalid"
	KindMissingIndex Kind = "missing_index"
	KindAlignment    Kind = "alignment"
	KindDependency   Kind = "dependency"
	KindCanceled     Kind = "canceled"
	KindResource     Kind = "resource_limit"
)

// Error is the structured error type used by avpipe internals.
type Error struct {
	Kind Kind `json:"kind"`
	// Offset is the box/field offset at which the failed read was attempted.
	Offset int64 `json:"offset,omitempty"`
	// FirstUnreadable is the first byte that is absent or cannot be decoded.
	// For a truncated buffer it is normally equal to the buffer length.
	FirstUnreadable int64  `json:"first_unreadable_offset,omitempty"`
	Component       string `json:"component,omitempty"`
	Message         string `json:"message"`
	Err             error  `json:"-"`
}

func (e *Error) Error() string {
	s := "fault"
	if e.Component != "" {
		s += " " + e.Component
	}
	if e.Kind != "" {
		s += " [" + string(e.Kind) + "]"
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.FirstUnreadable > 0 {
		s += fmt.Sprintf(" (first_unreadable_offset=%d)", e.FirstUnreadable)
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap exposes the wrapped error.
func (e *Error) Unwrap() error { return e.Err }

// New creates a structured error.
func New(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// Wrap wraps err in a structured Error.
func Wrap(kind Kind, err error, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...), Err: err}
}

// At attaches component and offset details and returns e.
func (e *Error) At(component string, offset, firstUnreadable int64) *Error {
	e.Component = component
	e.Offset = offset
	e.FirstUnreadable = firstUnreadable
	return e
}

// Truncated constructs a truncation error without performing an out-of-bounds
// read. offset is where the field began; firstUnreadable is the first absent
// byte (typically len(data)).
func Truncated(component string, fieldOffset, firstUnreadable int64, what string) *Error {
	return (&Error{
		Kind:            KindTruncated,
		Offset:          fieldOffset,
		FirstUnreadable: firstUnreadable,
		Component:       component,
		Message:         "truncated " + what,
	})
}

// OutOfBounds constructs an error for an invalid computed range.
func OutOfBounds(component string, offset int64, what string) *Error {
	return (&Error{
		Kind:      KindOutOfBounds,
		Offset:    offset,
		Component: component,
		Message:   "out of bounds " + what,
	})
}

// As extracts the deepest structured *Error in err's chain. The deepest
// error usually carries the precise parse offset; callers that need the
// outer classification can use errors.As directly.
func As(err error) (*Error, bool) {
	var detail *Error
	for err != nil {
		if fe, ok := err.(*Error); ok {
			detail = fe
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return detail, detail != nil
}

// ExitCode maps an error or isolation state to the terminal contract:
// 0 success; 1 arguments; 2 corrupt-but-isolated input; 3 dependency failure.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var fe *Error
	if errors.As(err, &fe) {
		switch fe.Kind {
		case KindArgument:
			return 1
		case KindDependency, KindCanceled:
			return 3
		case KindTruncated, KindOutOfBounds, KindInvalid, KindMissingIndex, KindAlignment:
			return 2
		default:
			return 1
		}
	}
	if errors.Is(err, ErrIsolatedCorruption) {
		return 2
	}
	return 1
}

// ErrIsolatedCorruption can be joined to a successful report when corruption
// was quarantined but the process should use exit code 2.
var ErrIsolatedCorruption = New(KindTruncated, "input corruption was isolated")
