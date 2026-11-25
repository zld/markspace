// Package fault provides the shared error classification used by every
// subsystem. Classified errors carry a stable machine-readable code, a
// recovery hint and enough context to trace the offending input.
package fault

import (
	"errors"
	"fmt"
)

// Code is a stable identifier for an error class. Acceptance checks and
// operators rely on these strings, so they must not be changed casually.
type Code string

const (
	// CodeTruncatedBox means an ISO BMFF box ends before its declared size or
	// a required box/field is missing from a fragment.
	CodeTruncatedBox Code = "truncated_box"
	// CodeSequenceGap means an mfhd sequence number is missing.
	CodeSequenceGap Code = "sequence_gap"
	// CodeDuplicateConflict means the same mfhd number appears twice with
	// different input digests or time ranges.
	CodeDuplicateConflict Code = "duplicate_conflict"
	// CodeTFDTRegression means baseMediaDecodeTime moved backwards.
	CodeTFDTRegression Code = "tfdt_regression"
	// CodeMisaligned means representation boundaries exceed the tolerance.
	CodeMisaligned Code = "misaligned"
	// CodeCheckpoint means checkpoint persistence or verification failed.
	CodeCheckpoint Code = "checkpoint"
	// CodeManifest means manifest generation or validation failed.
	CodeManifest Code = "manifest"
	// CodeCanceled means the pipeline was stopped before completion.
	CodeCanceled Code = "canceled"
	// CodeInvalidArgument means a CLI or API parameter is invalid.
	CodeInvalidArgument Code = "invalid_argument"
)

// Error is the classified error type shared across subsystems.
type Error struct {
	Code        Code
	Message     string
	Recoverable bool
	// Hint describes the safest recovery action for an operator or the
	// resume logic.
	Hint string
	// Input names the source (e.g. rep0/frag-002.m4s) the error refers to.
	Input string
	// Detail holds structured, deterministic extra fields (for example the
	// previous and regressed tfdt values).
	Detail map[string]any
}

func (e *Error) Error() string {
	if e.Input != "" {
		return fmt.Sprintf("%s: %s (input=%s)", e.Code, e.Message, e.Input)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// New builds a classified non-recoverable error without context.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Recoverable builds a recoverable classified error with a recovery hint.
func Recoverable(code Code, hint, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Recoverable: true, Hint: hint}
}

// WithInput attaches an input identifier and returns the same error.
func (e *Error) WithInput(input string) *Error {
	e.Input = input
	return e
}

// WithDetail attaches a structured detail field and returns the same error.
func (e *Error) WithDetail(key string, value any) *Error {
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	e.Detail[key] = value
	return e
}

// CodeOf extracts the classified Code from an error, returning an empty
// string for errors that are not of type *Error.
func CodeOf(err error) Code {
	var fe *Error
	if errors.As(err, &fe) {
		return fe.Code
	}
	return ""
}

// Is reports whether err carries the given code. It supports wrapped errors.
func Is(err error, code Code) bool {
	return CodeOf(err) == code
}
