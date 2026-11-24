// Package model defines the shared error model and common data structures
// used by every subsystem of the media processing kernel.
package model

import (
	"errors"
	"fmt"
)

// ErrorCode is the stable, machine-readable identifier of a failure class.
// The demo emitter reports these codes verbatim in its JSON summary.
type ErrorCode string

const (
	// ErrMetaUnparseable means a track's container metadata could not be parsed.
	ErrMetaUnparseable ErrorCode = "META_UNPARSEABLE"
	// ErrInvalidCrop means a crop rectangle is malformed or outside the frame.
	ErrInvalidCrop ErrorCode = "INVALID_CROP"
	// ErrTimestampOverflow means a timestamp cannot fit the shared time base.
	ErrTimestampOverflow ErrorCode = "TS_OVERFLOW"
	// ErrNonMonotonicDTS means a track's decoding timestamps go backwards.
	ErrNonMonotonicDTS ErrorCode = "NON_MONOTONIC_DTS"
	// ErrTimebaseMismatch means tracks declare incompatible time bases.
	ErrTimebaseMismatch ErrorCode = "TIMEBASE_MISMATCH"
	// ErrUnsupportedLayout means the channel layout has no normalization rule.
	ErrUnsupportedLayout ErrorCode = "UNSUPPORTED_LAYOUT"
	// ErrEmptyFrame means an audio frame carried zero samples.
	ErrEmptyFrame ErrorCode = "EMPTY_FRAME"
	// ErrRTPTruncated means an RTP packet is shorter than its header claims.
	ErrRTPTruncated ErrorCode = "RTP_TRUNCATED"
	// ErrLateRejected means an event arrived beyond the recompute window.
	ErrLateRejected ErrorCode = "LATE_REJECTED"
	// ErrBufferFull means the bounded stream rejected an item at its hard cap.
	ErrBufferFull ErrorCode = "BUFFER_FULL"
	// ErrStreamClosed means a producer wrote after the consumer had exited.
	ErrStreamClosed ErrorCode = "STREAM_CLOSED"
	// ErrContextCanceled means a blocked operation was released by cancellation.
	ErrContextCanceled ErrorCode = "CONTEXT_CANCELED"
)

// MediaError is the single structured error type of the kernel. Every failure
// that can be attributed to a specific track or byte offset carries that
// location so problems are diagnosable from the summary alone.
type MediaError struct {
	Code    ErrorCode `json:"code"`
	Track   int       `json:"track"`  // 1-based track number; 0 when not applicable.
	Offset  int64     `json:"offset"` // byte offset in the source; -1 when unknown.
	Message string    `json:"message"`
	Cause   error     `json:"-"`
}

// New builds a MediaError with an unknown offset.
func New(code ErrorCode, format string, args ...any) *MediaError {
	return &MediaError{Code: code, Track: 0, Offset: -1, Message: fmt.Sprintf(format, args...)}
}

// AtTrack returns a copy of the error annotated with a 1-based track number.
func (e *MediaError) AtTrack(track int) *MediaError {
	cp := *e
	cp.Track = track
	return &cp
}

// AtOffset returns a copy of the error annotated with a source byte offset.
func (e *MediaError) AtOffset(offset int64) *MediaError {
	cp := *e
	cp.Offset = offset
	return &cp
}

// Wrap records a causal error underneath the structured error.
func (e *MediaError) Wrap(cause error) *MediaError {
	cp := *e
	cp.Cause = cause
	return &cp
}

func (e *MediaError) Error() string {
	loc := ""
	if e.Track > 0 || e.Offset >= 0 {
		loc = fmt.Sprintf(" [track=%d offset=%d]", e.Track, e.Offset)
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s%s: %s: %v", e.Code, loc, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s%s: %s", e.Code, loc, e.Message)
}

func (e *MediaError) Unwrap() error { return e.Cause }

// AsMediaError extracts a *MediaError from an error chain, or returns nil.
func AsMediaError(err error) *MediaError {
	var me *MediaError
	if errors.As(err, &me) {
		return me
	}
	return nil
}

// HasCode reports whether err is a MediaError carrying the given code.
func HasCode(err error, code ErrorCode) bool {
	if me := AsMediaError(err); me != nil {
		return me.Code == code
	}
	return false
}
