package model

import (
	"errors"
	"fmt"
)

// ErrorClass is the stable, machine-readable classification used by every
// subsystem. The CLI maps Header to exit code 2 and usage errors to code 1.
type ErrorClass string

const (
	ErrorArgument    ErrorClass = "argument"
	ErrorHeader      ErrorClass = "header"
	ErrorTruncated   ErrorClass = "truncated_stream"
	ErrorCorrupt     ErrorClass = "corrupt_packet"
	ErrorLate        ErrorClass = "late_packet"
	ErrorCheckpoint  ErrorClass = "checkpoint"
	ErrorIndex       ErrorClass = "index"
	ErrorDigest      ErrorClass = "input_digest_mismatch"
	ErrorUnsupported ErrorClass = "unsupported"
)

// Error carries an optional byte/packet offset in addition to its class.
type Error struct {
	Class        ErrorClass `json:"class"`
	Offset       int64      `json:"offset"`
	PacketOffset int64      `json:"packet_offset,omitempty"`
	Message      string     `json:"message"`
	Err          error      `json:"-"`
}

func (e *Error) Error() string {
	where := ""
	if e.Offset >= 0 {
		where = fmt.Sprintf(" at_offset=%d", e.Offset)
	}
	if e.PacketOffset > 0 {
		where += fmt.Sprintf(" packet_offset=%d", e.PacketOffset)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s%s: %s: %v", e.Class, where, e.Message, e.Err)
	}
	return fmt.Sprintf("%s%s: %s", e.Class, where, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

func NewError(class ErrorClass, offset int64, msg string, err error) *Error {
	return &Error{Class: class, Offset: offset, Message: msg, Err: err}
}

func WithPacketOffset(err error, packetOffset int64) error {
	var me *Error
	if errors.As(err, &me) {
		me.PacketOffset = packetOffset
	}
	return err
}

func ClassOf(err error) ErrorClass {
	var me *Error
	if errors.As(err, &me) {
		return me.Class
	}
	return ""
}

func IsClass(err error, class ErrorClass) bool {
	return ClassOf(err) == class
}
