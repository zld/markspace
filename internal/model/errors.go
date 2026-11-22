package model

import "fmt"

// Category is the stable, machine-readable error class surfaced to the
// terminal and mapped onto process exit codes.
type Category string

const (
	// CategoryInput covers malformed descriptions, missing fields,
	// non-positive bandwidth, zero-duration segments and bad numerics.
	CategoryInput Category = "INPUT_ERROR"
	// CategoryAlignment covers boundaries that cannot be reconciled
	// within tolerance, missing common switch points and time overflow.
	CategoryAlignment Category = "ALIGNMENT_ERROR"
	// CategoryIO covers file writes, directory creation and XML failures.
	CategoryIO Category = "IO_ERROR"
)

// Stable error codes within a category.
const (
	CodeMissingField      = "MISSING_FIELD"
	CodeInvalidValue      = "INVALID_VALUE"
	CodeDuplicate         = "DUPLICATE_VALUE"
	CodeNotMonotonic      = "NOT_MONOTONIC"
	CodeUnknownField      = "UNKNOWN_FIELD"
	CodeOverflow          = "OVERFLOW"
	CodeNoSwitchPoint     = "NO_COMMON_SWITCH_POINT"
	CodeToleranceExceeded = "TOLERANCE_EXCEEDED"
	CodeBoundaryNotRA     = "BOUNDARY_NOT_RANDOM_ACCESS"
	CodePathConflict      = "PATH_CONFLICT"
	CodeWriteFailed       = "WRITE_FAILED"
)

// Error is the only structured error type returned by the pipeline.
type Error struct {
	Category Category
	Code     string
	Message  string
}

func (e *Error) Error() string {
	return string(e.Category) + "[" + e.Code + "]: " + e.Message
}

// Errorf constructs a categorized, formatted error.
func Errorf(cat Category, code, format string, args ...any) *Error {
	return &Error{Category: cat, Code: code, Message: fmt.Sprintf(format, args...)}
}

// AsError extracts a *model.Error from any error.
func AsError(err error) (*Error, bool) {
	if err == nil {
		return nil, false
	}
	if me, ok := err.(*Error); ok {
		return me, true
	}
	return nil, false
}

// ExitCode maps an error category onto the documented exit codes:
// input/alignment -> 2, IO -> 3, success -> 0.
func ExitCode(err error) int {
	me, ok := AsError(err)
	if !ok || me == nil {
		return 2
	}
	switch me.Category {
	case CategoryIO:
		return 3
	default:
		return 2
	}
}
