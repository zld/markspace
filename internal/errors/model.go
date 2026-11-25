// Package errs defines the single error model shared by every processing
// stage. Each error belongs to one of three classes:
//
//   - Recoverable: a local defect (bad NAL, oversized AU). The stream scan
//     keeps going and the affected range is flagged corrupt.
//   - Skip: an index entry or frame cannot be used; it is recorded with a
//     reason and never silently rebound to a neighbouring frame.
//   - Fatal: the whole run must stop (unreadable input, write failure,
//     checkpoint/input version mismatch).
package errs

import "fmt"

// Class partitions processing failures by recovery strategy.
type Class int

const (
	Recoverable Class = iota + 1
	Skip
	Fatal
)

func (c Class) String() string {
	switch c {
	case Recoverable:
		return "recoverable"
	case Skip:
		return "skip"
	case Fatal:
		return "fatal"
	default:
		return "unknown"
	}
}

// Code is a stable, machine-readable error identifier.
type Code string

const (
	CodeUnsupportedHeader  Code = "unsupported_header"
	CodeIncompleteHeader   Code = "incomplete_header"
	CodeTruncatedStream    Code = "truncated_stream"
	CodeOversizedAU        Code = "oversized_au"
	CodeOffsetOutOfRange   Code = "offset_out_of_range"
	CodeNonMonotonicIndex  Code = "non_monotonic_index"
	CodeWrongCodecType     Code = "wrong_codec_type"
	CodeIndexMismatch      Code = "index_mismatch"
	CodeCheckpointMismatch Code = "checkpoint_mismatch"
	CodeIOFailure          Code = "io_failure"
	CodeCancelled          Code = "cancelled"
	CodeInternal           Code = "internal"
)

// Error carries the class, stable code, byte offset where the defect was
// observed and a human-readable message. Offset is -1 when not applicable.
type Error struct {
	Class  Class  `json:"class"`
	Code   Code   `json:"code"`
	Offset int64  `json:"offset"`
	Msg    string `json:"msg"`
}

func (e *Error) Error() string {
	if e.Offset >= 0 {
		return fmt.Sprintf("%s/%s at offset %d: %s", e.Class, e.Code, e.Offset, e.Msg)
	}
	return fmt.Sprintf("%s/%s: %s", e.Class, e.Code, e.Msg)
}

// New builds an error without a byte offset.
func New(class Class, code Code, format string, args ...any) *Error {
	return &Error{Class: class, Offset: -1, Code: code, Msg: fmt.Sprintf(format, args...)}
}

// At builds an error anchored at a stream byte offset.
func At(class Class, code Code, offset int64, format string, args ...any) *Error {
	return &Error{Class: class, Offset: offset, Code: code, Msg: fmt.Sprintf(format, args...)}
}

// As extracts a *Error from any error, wrapping plain errors as fatal
// internal errors so callers always get the uniform model.
func As(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if AsError(err, &e) {
		return e
	}
	return New(Fatal, CodeInternal, "%v", err)
}
