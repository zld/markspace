// Package timeline defines the locatable, classified error model used by every
// media-processing subsystem and the conversion from per-track media clocks to
// one common microsecond time axis.
package timeline

import (
	"errors"
	"fmt"
)

// Stable error codes. They are part of the CLI/JSON contract, do not rename.
const (
	// ErrBoxTruncated: a box header or payload runs past the end of input.
	ErrBoxTruncated = "E_BOX_TRUNCATED"
	// ErrBadBoxLength: a box declares a length of zero/one-without-largesize or
	// a length inconsistent with its container.
	ErrBadBoxLength = "E_BAD_BOX_LENGTH"
	// ErrMissingField: a box required by the CMAF fragment model is absent.
	ErrMissingField = "E_MISSING_FIELD"
	// ErrSeqGap: a recoverable mfhd sequence-number gap.
	ErrSeqGap = "E_SEQ_GAP"
	// ErrSeqConflict: two fragments share a sequence number but differ in
	// content. The group must be aborted.
	ErrSeqConflict = "E_SEQ_CONFLICT"
	// ErrTimeline: a time interval cannot be mapped deterministically.
	ErrTimeline = "E_TIMELINE"
	// ErrLimitExceeded: a configured size/length/count limit was exceeded.
	ErrLimitExceeded = "E_LIMIT"
	// ErrRecovery: WAL/committed state does not match the current input
	// digest; resuming is refused.
	ErrRecovery = "E_WAL_MISMATCH"
	// ErrInvalidInput: any other malformed-input condition.
	ErrInvalidInput = "E_INVALID_INPUT"
)

// MediaError is the only error type carrying acceptance-level classification.
// Offset is the zero-based byte offset inside File, or -1 when not applicable.
type MediaError struct {
	Code    string
	File    string
	Offset  int64
	Message string
	Cause   error
}

// New builds a MediaError with an explicit location.
func New(code, file string, offset int64, format string, args ...any) *MediaError {
	return &MediaError{
		Code:    code,
		File:    file,
		Offset:  offset,
		Message: fmt.Sprintf(format, args...),
	}
}

func (e *MediaError) Error() string {
	loc := e.File
	if e.Offset >= 0 {
		loc = fmt.Sprintf("%s@%d", e.File, e.Offset)
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s %s: %s (%s)", e.Code, loc, e.Message, e.Cause.Error())
	}
	return fmt.Sprintf("%s %s: %s", e.Code, loc, e.Message)
}

// Unwrap exposes the wrapped cause for errors.Is/errors.As.
func (e *MediaError) Unwrap() error { return e.Cause }

// AsMediaError extracts a *MediaError from err, or wraps a plain error with
// ErrInvalidInput. It returns nil when err is nil.
func AsMediaError(err error) *MediaError {
	if err == nil {
		return nil
	}
	var me *MediaError
	if errors.As(err, &me) {
		return me
	}
	return &MediaError{Code: ErrInvalidInput, File: "", Offset: -1, Message: err.Error(), Cause: err}
}

// ExitCode maps an error to the documented process exit code:
// 0 success, 2 corrupt/input error, 3 recovery-summary mismatch.
// Usage errors (exit 1) are produced by the CLI itself, never here.
func ExitCode(err error) int {
	me := AsMediaError(err)
	if me == nil {
		return 0
	}
	if me.Code == ErrRecovery {
		return 3
	}
	return 2
}
