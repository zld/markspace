package model

import (
	"errors"
	"fmt"
)

// ErrorCode enumerates the structured, machine-readable failure causes.
type ErrorCode string

const (
	// CodeDigestMismatch: an input digest changed, the index must be rebuilt.
	CodeDigestMismatch ErrorCode = "digest_mismatch"
	// CodeIndexIncomplete: an index lacks its completion marker.
	CodeIndexIncomplete ErrorCode = "index_incomplete"
	// CodeOffsetOutOfRange: a referenced byte offset is outside the file.
	CodeOffsetOutOfRange ErrorCode = "offset_out_of_range"
	// CodeTruncated: a packet/tail block is shorter than its header promises.
	CodeTruncated ErrorCode = "truncated"
	// CodeIllegalTimestamp: an undecidable/illegal timestamp boundary.
	CodeIllegalTimestamp ErrorCode = "illegal_timestamp"
	// CodeInfeasible: no configuration can satisfy the requested constraints.
	CodeInfeasible ErrorCode = "infeasible"
	// CodeManifest: a manifest verification check failed.
	CodeManifest ErrorCode = "manifest_check_failed"
	// CodeState: an invalid pipeline state transition / corrupt state file.
	CodeState ErrorCode = "invalid_state"
	// CodeIO: an underlying filesystem/parsing failure.
	CodeIO ErrorCode = "io_error"
)

// Error is the structured error type carried across subsystems. Recoverable
// distinguishes a per-track, resumable defect from a fatal pipeline failure.
type Error struct {
	Code        ErrorCode `json:"code"`
	Track       int       `json:"track,omitempty"`
	Offset      int64     `json:"offset,omitempty"`
	Recoverable bool      `json:"recoverable"`
	Message     string    `json:"message"`
	Cause       error     `json:"-"`
}

func (e *Error) Error() string {
	loc := ""
	if e.Track != 0 || e.Offset != 0 {
		loc = fmt.Sprintf(" track=%d offset=%d", e.Track, e.Offset)
	}
	rec := "fatal"
	if e.Recoverable {
		rec = "recoverable"
	}
	return fmt.Sprintf("%s [%s]%s: %s", e.Code, rec, loc, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// NewError builds a structured error.
func NewError(code ErrorCode, recoverable bool, format string, args ...any) *Error {
	return &Error{Code: code, Recoverable: recoverable, Message: fmt.Sprintf(format, args...)}
}

// AtTrack annotates the error with a track and byte offset and returns it.
func (e *Error) AtTrack(track int, offset int64) *Error {
	e.Track, e.Offset = track, offset
	return e
}

// AsError extracts a *model.Error from any wrapped error.
func AsError(err error) (*Error, bool) {
	var me *Error
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}
