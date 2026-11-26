package model

import (
	"errors"
	"fmt"
)

// Code is a stable, machine-readable failure class. Terminal output prints it
// verbatim as code=<value> so regressions can be tracked across fixtures.
type Code string

const (
	CodeInvalidInput    Code = "invalid_input"
	CodeManifest        Code = "manifest_invalid"
	CodeUnsupported     Code = "unsupported_format"
	CodeTruncatedBox    Code = "truncated_box"
	CodeOffsetRange     Code = "offset_out_of_range"
	CodeAnchorAmbiguous Code = "anchor_ambiguous"
	CodeAnchorMismatch  Code = "anchor_mismatch"
	CodeNonMonotonic    Code = "non_monotonic"
	CodeQueueOverflow   Code = "queue_overflow"
	CodeFragmentTooBig  Code = "fragment_too_large"
	CodeDigestMismatch  Code = "digest_mismatch"
	CodeCacheCorrupt    Code = "cache_corrupt"
	CodeCheckpoint      Code = "checkpoint_invalid"
	CodeEvidence        Code = "evidence_failed"
	CodeInternal        Code = "internal"
	CodeInterrupted     Code = "interrupted"
)

// Err is the single error type that crosses subsystem boundaries. It always
// carries a code, the operation, and -- when known -- the byte offset and the
// media time range involved. Repair violations additionally name the source,
// fragment and track in Detail.
type Err struct {
	Code    Code
	Op      string
	Path    string
	Detail  string
	Offset  int64
	HasByte bool
	Start   int64
	End     int64
	HasTime bool
	Cause   error
}

// E starts an error with a code and operation.
func E(code Code, op, format string, args ...interface{}) *Err {
	return &Err{Code: code, Op: op, Detail: fmt.Sprintf(format, args...)}
}

// New is a shorthand for E(code, op, "%s", msg).
func New(code Code, op, msg string) *Err { return E(code, op, "%s", msg) }

// Error renders code, operation, location and detail in a stable order.
func (e *Err) Error() string {
	if e == nil {
		return "<nil>"
	}
	s := string(e.Code)
	if e.Op != "" {
		s += ": " + e.Op
	}
	if e.Path != "" {
		s += " " + e.Path
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	if e.HasByte {
		s += fmt.Sprintf(" (offset=%d)", e.Offset)
	}
	if e.HasTime {
		s += fmt.Sprintf(" (time=[%d,%d))", e.Start, e.End)
	}
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

func (e *Err) Unwrap() error { return e.Cause }

// WithPath attaches the file or artifact path.
func (e *Err) WithPath(p string) *Err { e.Path = p; return e }

// WithByte attaches the byte offset (tfdt field, box start, unreadable byte).
func (e *Err) WithByte(off int64) *Err { e.Offset, e.HasByte = off, true; return e }

// WithRange attaches the half-open media tick range involved.
func (e *Err) WithRange(start, end int64) *Err {
	e.Start, e.End, e.HasTime = start, end, true
	return e
}

// WithCause wraps an underlying error.
func (e *Err) WithCause(err error) *Err { e.Cause = err; return e }

// WithDetail overrides the formatted detail.
func (e *Err) WithDetail(format string, args ...interface{}) *Err {
	e.Detail = fmt.Sprintf(format, args...)
	return e
}

// ExitCode maps the error class to the documented CLI exit status. Truncated
// input is exit 2; contention/interruption is 75 (EX_TEMPFAIL); usage errors
// are 64; everything else is 1.
func (e *Err) ExitCode() int {
	if e == nil {
		return 0
	}
	switch e.Code {
	case CodeTruncatedBox:
		return 2
	case CodeInterrupted:
		return 75
	case CodeInvalidInput:
		return 64
	default:
		return 1
	}
}

// As extracts the typed error from a chain.
func As(err error) (*Err, bool) {
	var e *Err
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// ExitCodeOf is a convenience for the command layer.
func ExitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	if e, ok := As(err); ok {
		return e.ExitCode()
	}
	return 1
}

// ByteOffsetOf reports the first attached byte offset, if any.
func ByteOffsetOf(err error) (int64, bool) {
	if e, ok := As(err); ok && e.HasByte {
		return e.Offset, true
	}
	return 0, false
}
