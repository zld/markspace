// Package clock defines the unified integer media time base and the shared
// structured error type used by every media-kernel subsystem.
package clock

import (
	"errors"
	"fmt"
)

// Category is the machine-readable error class. The same vocabulary is used
// across RTP, WAV, index, meta, cache and pipeline stages so that a failure
// can be traced across module boundaries.
type Category string

const (
	CatInvalidParam   Category = "invalid_parameter"
	CatOverflow       Category = "overflow"
	CatUnmappableTime Category = "unmappable_time"
	CatTruncated      Category = "truncated"
	CatBadLength      Category = "bad_length"
	CatMisaligned     Category = "misaligned_frame"
	CatUnsupported    Category = "unsupported_encoding"
	CatLayoutMismatch Category = "layout_mismatch"
	CatCheckpoint     Category = "checkpoint_corrupt"
	CatCacheMismatch  Category = "cache_mismatch"
	CatChecksum       Category = "checksum_mismatch"
	CatNotFound       Category = "not_found"
	CatSymlinkEscape  Category = "symlink_escape"
)

// Error is the single structured error shape shared by all subsystems.
// ByteOffset locates a WAV/index failure inside the input file; SeqNum
// locates an RTP failure on the offending packet. The unused locator is
// explicitly marked so callers never confuse zero with a real position.
type Error struct {
	Stage      string
	Category   Category
	Message    string
	ByteOffset int64
	HasOffset  bool
	SeqNum     uint16
	HasSeq     bool
	Cause      error
}

// NewError builds a structured stage error. ByteOffset starts as -1/absent.
func NewError(stage string, cat Category, format string, args ...any) *Error {
	return &Error{
		Stage:      stage,
		Category:   cat,
		Message:    fmt.Sprintf(format, args...),
		ByteOffset: -1,
	}
}

// At attaches the first offending input byte offset and returns the error.
func (e *Error) At(offset int64) *Error {
	e.ByteOffset = offset
	e.HasOffset = true
	return e
}

// WithSeq attaches the offending RTP packet sequence number.
func (e *Error) WithSeq(seq uint16) *Error {
	e.SeqNum = seq
	e.HasSeq = true
	return e
}

// Wrap retains the underlying cause while preserving the structured fields.
func (e *Error) Wrap(cause error) *Error {
	e.Cause = cause
	return e
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%s: %s: %s", e.Stage, e.Category, e.Message)
	if e.HasOffset {
		s += fmt.Sprintf(" (byte_offset=%d)", e.ByteOffset)
	}
	if e.HasSeq {
		s += fmt.Sprintf(" (seq=%d)", e.SeqNum)
	}
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

// Unwrap exposes the wrapped cause to errors.Is/errors.As.
func (e *Error) Unwrap() error { return e.Cause }

// AsError extracts a *clock.Error from any wrapped error chain.
func AsError(err error) (*Error, bool) {
	var ce *Error
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}
