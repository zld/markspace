package model

import (
	"errors"
	"fmt"
)

// TrackID identifies a media, subtitle, or timed-metadata track.
type TrackID string

// ErrorCategory is the stable, machine-readable class of a failure.
type ErrorCategory string

const (
	CategoryInvalidArgument    ErrorCategory = "invalid_argument"
	CategoryAmbiguousTimestamp ErrorCategory = "ambiguous_timestamp"
	CategoryMissingDependency  ErrorCategory = "missing_dependency"
	CategoryDependencyCycle    ErrorCategory = "dependency_cycle"
	CategoryNotReady           ErrorCategory = "dependency_not_ready"
	CategoryQueueBackpressure  ErrorCategory = "queue_backpressure"
	CategoryUnitTooLarge       ErrorCategory = "unit_too_large"
	CategoryTruncatedBox       ErrorCategory = "truncated_box"
	CategoryInvalidLength      ErrorCategory = "invalid_length"
	CategoryMissingKeyframe    ErrorCategory = "missing_keyframe"
	CategoryIntegrityMismatch  ErrorCategory = "integrity_mismatch"
	CategoryChecksumMismatch   ErrorCategory = "checksum_mismatch"
	CategoryPathTraversal      ErrorCategory = "path_traversal"
	CategoryUnsafeResume       ErrorCategory = "unsafe_resume"
	CategoryOutputWrite        ErrorCategory = "output_write"
)

// Error carries the track and source position needed to audit a media failure.
// Offset is -1 when the failure has no byte offset.
type Error struct {
	Track       TrackID       `json:"track,omitempty"`
	Source      string        `json:"source,omitempty"`
	Offset      int64         `json:"offset"`
	Category    ErrorCategory `json:"category"`
	Message     string        `json:"message"`
	Recoverable bool          `json:"recoverable"`
	Warning     bool          `json:"warning,omitempty"`
	Err         error         `json:"-"`
}

func (e *Error) Error() string {
	where := ""
	if e.Track != "" {
		where += " track=" + string(e.Track)
	}
	if e.Source != "" {
		where += " source=" + e.Source
	}
	if e.Offset >= 0 {
		where += fmt.Sprintf(" offset=%d", e.Offset)
	}
	msg := e.Message
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return fmt.Sprintf("%s%s: %s", e.Category, where, msg)
}

func (e *Error) Unwrap() error { return e.Err }

// NewError constructs a categorized media error.
func NewError(track TrackID, source string, offset int64, category ErrorCategory, message string, cause error) *Error {
	return &Error{
		Track:    track,
		Source:   source,
		Offset:   offset,
		Category: category,
		Message:  message,
		Err:      cause,
	}
}

// Recoverable marks an error as safe to warn about and continue around.
func (e *Error) RecoverableWarn() *Error {
	e.Recoverable = true
	e.Warning = true
	return e
}

// AsError returns the *Error in err's chain, if present.
func AsError(err error) (*Error, bool) {
	var me *Error
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// IsCategory reports whether err contains category c.
func IsCategory(err error, c ErrorCategory) bool {
	me, ok := AsError(err)
	return ok && me.Category == c
}
