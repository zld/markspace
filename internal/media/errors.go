package media

import (
	"errors"
	"fmt"
)

// ErrorClass partitions failures by recoverability so the CLI can map them
// onto stable process exit codes.
type ErrorClass int

const (
	ClassInvalidArgument ErrorClass = iota + 1 // bad CLI input / unknown version (exit 2)
	ClassRecoverable                           // truncated or corrupt stream region (exit 3)
	ClassCheckpoint                            // checkpoint / result verification failure (exit 4)
	ClassFatal                                 // unrecoverable internal failure
)

// ExitCode maps an error class onto the documented archivectl exit code.
func (c ErrorClass) ExitCode() int {
	switch c {
	case ClassInvalidArgument:
		return 2
	case ClassRecoverable:
		return 3
	case ClassCheckpoint:
		return 4
	default:
		return 1
	}
}

// Error is the layered error model used across every subsystem. It carries a
// class, the subsystem that raised it, the byte offset involved and the
// wrapped cause.
type Error struct {
	Class   ErrorClass
	Subsys  string
	Offset  int64
	Message string
	Cause   error
}

func (e *Error) Error() string {
	loc := ""
	if e.Offset >= 0 {
		loc = fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s%s: %v", e.Subsys, e.Message, loc, e.Cause)
	}
	return fmt.Sprintf("%s: %s%s", e.Subsys, e.Message, loc)
}

func (e *Error) Unwrap() error { return e.Cause }

// NewError builds a classified error with no particular byte offset.
func NewError(class ErrorClass, subsys, msg string) *Error {
	return &Error{Class: class, Subsys: subsys, Offset: -1, Message: msg}
}

// NewOffsetError builds a classified error anchored at a byte offset.
func NewOffsetError(class ErrorClass, subsys, msg string, offset int64) *Error {
	return &Error{Class: class, Subsys: subsys, Offset: offset, Message: msg}
}

// WrapError attaches a cause to a classified error.
func WrapError(class ErrorClass, subsys, msg string, cause error) *Error {
	return &Error{Class: class, Subsys: subsys, Offset: -1, Message: msg, Cause: cause}
}

// Sentinel errors used with errors.Is for class-level checks.
var (
	ErrInvalidArgument = NewError(ClassInvalidArgument, "media", "invalid argument")
	ErrTruncated       = NewError(ClassRecoverable, "media", "truncated coding unit")
	ErrCheckpoint      = NewError(ClassCheckpoint, "media", "checkpoint mismatch")
)

// UnitError records a per-unit parse failure without aborting the whole job.
type UnitError struct {
	Offset  int64
	Length  int64
	Class   ErrorClass
	Reason  string
	RawHint string
}

// ClassOf returns the class of any error, defaulting to fatal.
func ClassOf(err error) ErrorClass {
	var me *Error
	if errors.As(err, &me) {
		return me.Class
	}
	return ClassFatal
}
