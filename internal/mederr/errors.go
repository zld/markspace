// Package mederr defines the structured error model shared by every medtrace
// subsystem. Errors carry a machine-readable category, a byte offset into the
// offending input and the EBML element path, so failures can be reported
// deterministically and mapped to process exit codes.
package mederr

import (
	"errors"
	"fmt"
	"strings"
)

// Category classifies every structured error produced by medtrace.
type Category string

const (
	// CatInvalidVINT: an EBML variable-size integer violates the VINT rules.
	CatInvalidVINT Category = "invalid_vint"
	// CatTruncatedHeader: an element header or body runs past the parent/EOF.
	CatTruncatedHeader Category = "truncated_header"
	// CatLengthOutOfBounds: a declared element length crosses its parent.
	CatLengthOutOfBounds Category = "length_out_of_bounds"
	// CatUnknownLength: an unknown-length element appears where it is illegal.
	CatUnknownLength Category = "unknown_length_element"
	// CatOverflow: an arithmetic conversion would overflow an integer.
	CatOverflow Category = "arithmetic_overflow"
	// CatInvalidInput: malformed event records or media blocks.
	CatInvalidInput Category = "invalid_input"
	// CatCycle: a track dependency graph contains a cycle.
	CatCycle Category = "dependency_cycle"
	// CatStateCorrupt: a checkpoint cannot be validated.
	CatStateCorrupt Category = "state_corrupt"
	// CatIO: an operating-system level I/O failure.
	CatIO Category = "io_error"
)

// Error is the structured error type used across all subsystems.
type Error struct {
	Category Category
	Offset   int64    // byte offset in the input; -1 when not applicable
	Path     []string // EBML element path, outer first
	Message  string
	Cause    error
}

// New builds an Error with no byte offset (Offset = -1).
func New(cat Category, format string, args ...any) *Error {
	return &Error{Category: cat, Offset: -1, Message: fmt.Sprintf(format, args...)}
}

// At builds an Error anchored at a byte offset.
func At(cat Category, offset int64, format string, args ...any) *Error {
	return &Error{Category: cat, Offset: offset, Message: fmt.Sprintf(format, args...)}
}

// WithPath returns a copy of e annotated with the given element path.
func (e *Error) WithPath(path []string) *Error {
	cp := *e
	cp.Path = append([]string(nil), path...)
	return &cp
}

// Wrap attaches a cause while preserving the structured fields.
func (e *Error) Wrap(cause error) *Error {
	cp := *e
	cp.Cause = cause
	return &cp
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s", e.Category, e.Message)
	if e.Offset >= 0 {
		fmt.Fprintf(&b, " (offset=%d)", e.Offset)
	}
	if len(e.Path) > 0 {
		fmt.Fprintf(&b, " path=%s", strings.Join(e.Path, "/"))
	}
	if e.Cause != nil {
		fmt.Fprintf(&b, ": %v", e.Cause)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Cause }

// AsError extracts a *Error from any error, returning nil if absent.
func AsError(err error) *Error {
	var se *Error
	if errors.As(err, &se) {
		return se
	}
	return nil
}

// ExitCode maps an error to the documented medtrace process exit code.
//
//	0 success, 2 input/format error, 3 state corruption unrecoverable.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if e := AsError(err); e != nil && e.Category == CatStateCorrupt {
		return 3
	}
	return 2
}
