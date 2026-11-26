package timebase

import (
	"errors"
	"fmt"
)

// Code is a stable machine-readable error code. Codes are part of the CLI
// contract: they appear in verify output and must not be renamed casually.
type Code string

const (
	CodeBoxTruncated      Code = "BOX_TRUNCATED"
	CodeTimeRegression    Code = "TIME_REGRESSION"
	CodeTimeOverflow      Code = "TIME_OVERFLOW"
	CodeEventOutOfRange   Code = "EVENT_OUT_OF_RANGE"
	CodeCorruptField      Code = "CORRUPT_FIELD"
	CodeUnsupportedFormat Code = "UNSUPPORTED_FORMAT"
	CodeByteLimit         Code = "BYTE_LIMIT_EXCEEDED"
	CodeNoKeyframe        Code = "NO_KEYFRAME"
	CodeQualityGate       Code = "QUALITY_GATE_FAILED"
	CodeBudgetExceeded    Code = "BUDGET_EXCEEDED"
	CodeOutputTooLarge    Code = "OUTPUT_TOO_LARGE"
	CodeManifestInvalid   Code = "MANIFEST_INVALID"
	CodeManifestVersion   Code = "MANIFEST_VERSION_UNSUPPORTED"
	CodeDigestMismatch    Code = "DIGEST_MISMATCH"
	CodeCommitConflict    Code = "COMMIT_CONFLICT"
	CodeInterrupted       Code = "INTERRUPTED"
	CodeInternal          Code = "INTERNAL"
)

// Error carries a stable code plus the track and byte offset that anchor it
// in the source media. Track is 0 when the error is not track-scoped; Offset
// is -1 when no meaningful byte offset exists.
type Error struct {
	Code    Code   `json:"code"`
	TrackID uint32 `json:"track,omitempty"`
	Offset  int64  `json:"offset"`
	Message string `json:"message"`
	Cause   error  `json:"-"`
}

// New builds an Error with a formatted message.
func NewError(code Code, track uint32, offset int64) *Error {
	return &Error{Code: code, TrackID: track, Offset: offset}
}

// Newf builds an Error with a formatted message.
func Newf(code Code, track uint32, offset int64, format string, args ...any) *Error {
	return &Error{Code: code, TrackID: track, Offset: offset, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches a cause and optional message.
func (e *Error) Wrap(cause error, format string, args ...any) *Error {
	cp := *e
	cp.Cause = cause
	if format != "" {
		if cp.Message != "" {
			cp.Message = fmt.Sprintf(format, args...) + ": " + cp.Message
		} else {
			cp.Message = fmt.Sprintf(format, args...)
		}
	}
	return &cp
}

// WithTrack returns a copy scoped to the given track.
func (e *Error) WithTrack(track uint32) *Error {
	cp := *e
	cp.TrackID = track
	return &cp
}

// WithOffset returns a copy anchored at the given byte offset.
func (e *Error) WithOffset(offset int64) *Error {
	cp := *e
	cp.Offset = offset
	return &cp
}

// Error implements error. Format: CODE track=N offset=M: message.
func (e *Error) Error() string {
	loc := fmt.Sprintf("track=%d offset=%d", e.TrackID, e.Offset)
	if e.TrackID == 0 {
		loc = fmt.Sprintf("offset=%d", e.Offset)
	}
	if e.Message == "" {
		return fmt.Sprintf("%s %s", e.Code, loc)
	}
	return fmt.Sprintf("%s %s: %s", e.Code, loc, e.Message)
}

// Unwrap exposes the underlying cause to errors.Is/As.
func (e *Error) Unwrap() error { return e.Cause }

// AsError extracts a *Error from err, wrapping other errors as CodeInternal.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeInternal, Offset: -1, Message: err.Error(), Cause: err}
}

// HasCode reports whether err carries the given stable code.
func HasCode(err error, code Code) bool {
	if err == nil {
		return false
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code == code
	}
	return false
}
