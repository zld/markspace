// Package fault defines the stable error codes shared by every
// subsystem. Each Error carries the first offending input byte offset
// so a failed release can be traced back to the exact input position.
package fault

import (
	"errors"
	"fmt"
)

// Code is a stable, machine-readable error identifier.
type Code string

const (
	ErrTruncatedUnit   Code = "E_TRUNCATED_UNIT"   // unit cut off before its declared end
	ErrBadHeader       Code = "E_BAD_HEADER"       // forbidden bit set / reserved type
	ErrIndexOutOfRange Code = "E_INDEX_OUT_OF_RANGE"
	ErrMissingTrack    Code = "E_MISSING_TRACK"
	ErrDuplicateMap    Code = "E_DUPLICATE_MAPPING"
	ErrTimeRegression  Code = "E_TIME_REGRESSION"
	ErrSliceOutOfRange Code = "E_SLICE_OUT_OF_RANGE"
	ErrRTPBoundary     Code = "E_RTP_BOUNDARY"
	ErrUnknownField    Code = "E_UNKNOWN_FIELD"
	ErrBadVersion      Code = "E_UNSUPPORTED_VERSION"
	ErrBudgetExceeded  Code = "E_BUDGET_EXCEEDED"
	ErrInterrupted     Code = "E_INTERRUPTED"
	ErrLimitExceeded   Code = "E_LIMIT_EXCEEDED"
	ErrIntegrity       Code = "E_INTEGRITY"
	ErrQualityGate     Code = "E_QUALITY_GATE"
)

// NoOffset marks errors that are not tied to a byte position.
const NoOffset = int64(-1)

// Error couples a stable Code with the first offending input offset.
type Error struct {
	Code   Code
	Offset int64
	Msg    string
}

func (e *Error) Error() string {
	if e.Offset >= 0 {
		return fmt.Sprintf("%s offset=%d: %s", e.Code, e.Offset, e.Msg)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Msg)
}

// New builds an Error; pass NoOffset when not byte-related.
func New(c Code, off int64, format string, args ...any) *Error {
	return &Error{Code: c, Offset: off, Msg: fmt.Sprintf(format, args...)}
}

// HasCode reports whether err wraps an *Error with code c.
func HasCode(err error, c Code) bool {
	var fe *Error
	if errors.As(err, &fe) {
		return fe.Code == c
	}
	return false
}

// ExitCode maps an error to the process exit code contract:
// 0 success, 2 input error, 3 processing interruption.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case HasCode(err, ErrInterrupted), HasCode(err, ErrBudgetExceeded):
		return 3
	default:
		return 2
	}
}
