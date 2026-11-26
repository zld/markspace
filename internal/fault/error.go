// Package fault defines stable error codes shared by parsers, planner,
// pipeline and the independent validator.
package fault

import (
	"errors"
	"fmt"
)

// Code is a stable machine-readable failure classification.
type Code string

const (
	OK                  Code = "OK"
	InvalidArgument     Code = "E_INVALID_ARGUMENT"
	InputTooLarge       Code = "E_INPUT_TOO_LARGE"
	UnsupportedFormat   Code = "E_UNSUPPORTED_FORMAT"
	EBMLHeader          Code = "E_EBML_HEADER"
	EBMLVINT            Code = "E_EBML_VINT"
	EBMLBadBlock        Code = "E_EBML_BAD_BLOCK"
	EBMLResyncFailed    Code = "E_EBML_RESYNC_FAILED"
	TooManyTracks       Code = "E_TOO_MANY_TRACKS"
	TimeUnmappable      Code = "E_TIME_UNMAPPABLE"
	BudgetExceeded      Code = "E_BUDGET_EXCEEDED"
	NoCandidate         Code = "E_NO_CANDIDATE"
	DecodeFailed        Code = "E_DECODE_FAILED"
	CheckpointCorrupt   Code = "E_CHECKPOINT_CORRUPT"
	CheckpointMismatch  Code = "E_CHECKPOINT_MISMATCH"
	VerificationFailed  Code = "E_VERIFICATION_FAILED"
	ExternalToolMissing Code = "E_EXTERNAL_TOOL_MISSING"
)

// Error carries a code and optional byte offset. Offset is -1 when unknown.
type Error struct {
	Code      Code
	Offset    int64
	Err       error
	Recovered bool
}

func (e *Error) Error() string {
	where := ""
	if e.Offset >= 0 {
		where = fmt.Sprintf(" at offset %d", e.Offset)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s%s: %v", e.Code, where, e.Err)
	}
	return fmt.Sprintf("%s%s", e.Code, where)
}

func (e *Error) Unwrap() error { return e.Err }

// New constructs an error with an unknown offset.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Offset: -1, Err: fmt.Errorf(format, args...)}
}

// At constructs an error anchored to a file offset.
func At(code Code, offset int64, format string, args ...any) *Error {
	return &Error{Code: code, Offset: offset, Err: fmt.Errorf(format, args...)}
}

// Wrap preserves a code while wrapping another error.
func Wrap(code Code, err error) *Error {
	var fe *Error
	if errors.As(err, &fe) {
		return fe
	}
	return &Error{Code: code, Offset: -1, Err: err}
}

// IsCode reports whether err (or any wrapped error) has code.
func IsCode(err error, code Code) bool {
	var fe *Error
	return errors.As(err, &fe) && fe.Code == code
}

// Record is a recoverable parsing or processing observation.
type Record struct {
	Code    Code   `json:"code"`
	Offset  int64  `json:"offset"`
	Message string `json:"message"`
	StartNS int64  `json:"start_ns,omitempty"`
	EndNS   int64  `json:"end_ns,omitempty"`
}

// FromError converts an error to a serializable record.
func FromError(err error) Record {
	var fe *Error
	if errors.As(err, &fe) {
		msg := ""
		if fe.Err != nil {
			msg = fe.Err.Error()
		}
		return Record{Code: fe.Code, Offset: fe.Offset, Message: msg}
	}
	return Record{Code: InvalidArgument, Offset: -1, Message: err.Error()}
}
