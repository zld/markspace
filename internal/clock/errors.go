// Package clock defines the shared 90 kHz media clock, sample-rate
// conversions with remainder tracking, and the structured error type used
// by every subsystem of the mediasync library.
package clock

import "fmt"

// ErrorCode is the machine-readable category of a structured media error.
type ErrorCode string

const (
	ErrInvalidArgument ErrorCode = "invalid_argument" // bad parameter / header field
	ErrTruncated       ErrorCode = "truncated"        // input ends before declared length
	ErrOverflow        ErrorCode = "overflow"         // integer overflow or size limit breach
	ErrChecksum        ErrorCode = "checksum"         // checksum / digest mismatch
	ErrBlocked         ErrorCode = "blocked"          // timeline cannot be inferred
	ErrCorrupt         ErrorCode = "corrupt"          // unparseable / damaged record
	ErrDependency      ErrorCode = "dependency"       // upstream DAG dependency failed
	ErrLimitExceeded   ErrorCode = "limit_exceeded"   // retry / resource limit reached
)

// MediaError is the single structured error type shared by all subsystems.
// It always carries a stable code, a human-readable message and the byte
// offset (or sample/record index, depending on context) where the condition
// was detected. Missing is non-zero when the condition implies that a known
// number of bytes/items are absent.
type MediaError struct {
	Code    ErrorCode
	Message string
	Offset  int64
	Missing int64
	Cause   error
}

func (e *MediaError) Error() string {
	s := string(e.Code) + ": " + e.Message
	if e.Offset != 0 {
		s += fmt.Sprintf(" (offset=%d)", e.Offset)
	}
	if e.Missing > 0 {
		s += fmt.Sprintf(" (missing=%d)", e.Missing)
	}
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

// Unwrap exposes the wrapped cause for errors.Is/errors.As.
func (e *MediaError) Unwrap() error { return e.Cause }

// NewError builds a MediaError without positional detail.
func NewError(code ErrorCode, format string, args ...any) *MediaError {
	return &MediaError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrorAt builds a MediaError anchored at a byte offset / index.
func ErrorAt(code ErrorCode, offset int64, format string, args ...any) *MediaError {
	return &MediaError{Code: code, Message: fmt.Sprintf(format, args...), Offset: offset}
}

// TruncationError reports a truncated input with the offset where data
// stopped and the number of missing bytes.
func TruncationError(offset, missing int64, what string) *MediaError {
	return &MediaError{
		Code:    ErrTruncated,
		Message: what + " truncated",
		Offset:  offset,
		Missing: missing,
	}
}

// AsMediaError extracts a *MediaError from err, wrapping err if needed so
// callers always get a structured error with a code.
func AsMediaError(err error) *MediaError {
	if err == nil {
		return nil
	}
	if me, ok := err.(*MediaError); ok {
		return me
	}
	return &MediaError{Code: ErrCorrupt, Message: err.Error(), Cause: err}
}
