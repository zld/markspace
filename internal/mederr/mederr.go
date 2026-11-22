// Package mederr defines the unified media-processing error model.
//
// Every error carries an error code, the originating track, a byte offset,
// a time interval (in unified nanoseconds) and whether the caller may
// attempt recovery. Errors are constructed with the helpers in this file
// and inspected with As/Is.
package mederr

import (
	"errors"
	"fmt"
)

// ErrorCode is the stable, machine-readable classification of a failure.
type ErrorCode string

const (
	CodeInvalidManifest    ErrorCode = "invalid_manifest"
	CodeZeroTimescale      ErrorCode = "zero_timescale"
	CodeDuplicateNumber    ErrorCode = "duplicate_segment_number"
	CodeArithmeticOverflow ErrorCode = "arithmetic_overflow"
	CodeTruncatedSegment   ErrorCode = "truncated_segment"
	CodeUnrecoverableGap   ErrorCode = "unrecoverable_gap"
	CodeCheckpointCorrupt  ErrorCode = "checkpoint_corrupt"
	CodePathViolation      ErrorCode = "path_violation"
	CodeLimitExceeded      ErrorCode = "limit_exceeded"
	CodeInternal           ErrorCode = "internal"
)

// Process exit codes mandated by the specification.
const (
	ExitOK            = 0
	ExitInternal      = 1
	ExitInvalidInput  = 2
	ExitUnrecoverable = 3
)

// MediaError is the canonical error type shared by every subsystem.
type MediaError struct {
	Code        ErrorCode
	Track       string
	ByteOffset  int64
	StartNS     int64
	EndNS       int64
	Recoverable bool
	Message     string
}

func (e *MediaError) Error() string {
	s := fmt.Sprintf("%s: %s", e.Code, e.Message)
	if e.Track != "" {
		s += fmt.Sprintf(" track=%s", e.Track)
	}
	if e.ByteOffset >= 0 {
		s += fmt.Sprintf(" offset=%d", e.ByteOffset)
	}
	if e.StartNS != 0 || e.EndNS != 0 {
		s += fmt.Sprintf(" interval=[%d,%d)ns", e.StartNS, e.EndNS)
	}
	s += fmt.Sprintf(" recoverable=%t", e.Recoverable)
	return s
}

// New builds a MediaError. ByteOffset defaults to -1 when unknown.
func New(code ErrorCode, msg string, opts ...Option) *MediaError {
	e := &MediaError{Code: code, ByteOffset: -1, Message: msg}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Option customises a MediaError at construction.
type Option func(*MediaError)

func WithTrack(t string) Option { return func(e *MediaError) { e.Track = t } }
func WithOffset(off int64) Option {
	return func(e *MediaError) { e.ByteOffset = off }
}
func WithInterval(start, end int64) Option {
	return func(e *MediaError) { e.StartNS, e.EndNS = start, end }
}
func WithRecoverable() Option { return func(e *MediaError) { e.Recoverable = true } }

// AsMediaError extracts a *MediaError from any wrapped error chain.
func AsMediaError(err error) (*MediaError, bool) {
	var me *MediaError
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// HasCode reports whether err (or any wrapped error) carries the given code.
func HasCode(err error, code ErrorCode) bool {
	if me, ok := AsMediaError(err); ok {
		return me.Code == code
	}
	return false
}

// ExitCode maps an error to the mandated process exit code.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	me, ok := AsMediaError(err)
	if !ok {
		return ExitInternal
	}
	switch me.Code {
	case CodeInvalidManifest, CodeZeroTimescale, CodeDuplicateNumber,
		CodePathViolation, CodeLimitExceeded:
		return ExitInvalidInput
	case CodeTruncatedSegment, CodeUnrecoverableGap, CodeCheckpointCorrupt:
		return ExitUnrecoverable
	default:
		return ExitInternal
	}
}
