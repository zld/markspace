// Package model provides the shared error model for the media recovery kernel.
//
// Every subsystem reports failures through Error so that a caller can decide
// with errors.Is / errors.As whether a problem aborts the whole job (fatal) or
// merely degrades one recovered segment (recoverable). Errors are stable:
// the Code never depends on formatting, file paths or locale.
package model

import (
	"errors"
	"fmt"
)

// Code is a stable, machine-readable error identity.
type Code string

// Severity classifies how a caller should react to an Error.
type Severity int

const (
	// SeverityRecoverable means the pipeline can keep going and mark a segment.
	SeverityRecoverable Severity = iota
	// SeverityFatal means the job cannot produce a trustworthy result.
	SeverityFatal
)

// String renders the severity for logs and the acceptance report.
func (s Severity) String() string {
	switch s {
	case SeverityFatal:
		return "fatal"
	case SeverityRecoverable:
		return "recoverable"
	default:
		return "unknown"
	}
}

// Error is the single error type shared by every subsystem.
type Error struct {
	Code     Code
	Severity Severity
	// Offset is a byte offset into the source input when known, else -1.
	Offset int64
	// Stream names the subsystem, e.g. "subtitle", "rtp", "container".
	Stream string
	// Detail carries a human readable explanation.
	Detail string
	// Err is the wrapped cause, if any.
	Err error
}

// New builds a recoverable error.
func New(code Code, stream, detail string) *Error {
	return &Error{Code: code, Severity: SeverityRecoverable, Offset: -1, Stream: stream, Detail: detail}
}

// Fatal builds a fatal error.
func Fatal(code Code, stream, detail string) *Error {
	return &Error{Code: code, Severity: SeverityFatal, Offset: -1, Stream: stream, Detail: detail}
}

// WithOffset returns a shallow copy carrying a byte offset.
func (e *Error) WithOffset(off int64) *Error {
	c := *e
	c.Offset = off
	return &c
}

// WithFatal returns a shallow copy with the severity raised to fatal.
func (e *Error) WithFatal() *Error {
	c := *e
	c.Severity = SeverityFatal
	return &c
}

// WithCause returns a shallow copy wrapping err.
func (e *Error) WithCause(err error) *Error {
	c := *e
	c.Err = err
	return &c
}

// Error implements error.
func (e *Error) Error() string {
	loc := e.Stream
	if loc == "" {
		loc = "mediarc"
	}
	msg := fmt.Sprintf("%s: %s: %s", loc, e.Code, e.Detail)
	if e.Offset >= 0 {
		msg = fmt.Sprintf("%s: %s: offset=%d: %s", loc, e.Code, e.Offset, e.Detail)
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap exposes the cause for errors.Is/As.
func (e *Error) Unwrap() error { return e.Err }

// IsCode reports whether err carries the given stable code.
func IsCode(err error, code Code) bool {
	var target *Error
	if errors.As(err, &target) {
		return target.Code == code
	}
	return false
}

// CodeOf extracts the stable code from err, or "" when err is not a model error.
func CodeOf(err error) Code {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

// IsFatal reports whether err is a fatal model error.
func IsFatal(err error) bool {
	var target *Error
	if errors.As(err, &target) {
		return target.Severity == SeverityFatal
	}
	return err != nil
}

// OffsetOf returns the byte offset carried by err, or -1.
func OffsetOf(err error) int64 {
	var target *Error
	if errors.As(err, &target) {
		return target.Offset
	}
	return -1
}
