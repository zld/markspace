package model

import "fmt"

// ErrorType is the stable, machine-readable classification used both for
// terminal reporting and for choosing the process exit code.
type ErrorType string

const (
	ErrMissingFrame   ErrorType = "missing_frame"
	ErrTruncatedFrame ErrorType = "truncated_frame"
	ErrHashMismatch   ErrorType = "hash_mismatch"
	ErrTimestampBack  ErrorType = "timestamp_regression"
	ErrUnknownColor   ErrorType = "unknown_color_label"
	ErrGraphCycle     ErrorType = "dependency_cycle"
	ErrMissingDep     ErrorType = "missing_dependency"
	ErrDuplicateNode  ErrorType = "duplicate_node"
	ErrInvalidConfig  ErrorType = "invalid_config"
	ErrIO             ErrorType = "io_failure"
)

// MediaError attaches a stable classification and a precise location
// (track id, frame index, byte offset) to every pipeline failure. The zero
// value of FrameIndex / Offset means "not applicable".
type MediaError struct {
	Type       ErrorType `json:"type"`
	TrackID    string    `json:"track_id,omitempty"`
	FrameIndex int       `json:"frame_index,omitempty"`
	Offset     int64     `json:"offset_bytes,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}

// Error implements the error interface with a human-readable location line.
func (e *MediaError) Error() string {
	loc := e.TrackID
	if loc != "" {
		loc = fmt.Sprintf("track=%s frame=%d", e.TrackID, e.FrameIndex)
		if e.Offset > 0 {
			loc += fmt.Sprintf(" offset=%d", e.Offset)
		}
	}
	if loc == "" {
		loc = e.Detail
	} else if e.Detail != "" {
		loc += ": " + e.Detail
	}
	return fmt.Sprintf("%s at %s", e.Type, loc)
}

// ExitCode maps an error type to the contract's process exit codes:
// input corruption that was accurately reported -> 2, invalid parameters
// or dependency graph problems -> 1, success -> 0.
func (e *MediaError) ExitCode() int {
	switch e.Type {
	case ErrMissingFrame, ErrTruncatedFrame, ErrHashMismatch,
		ErrTimestampBack, ErrUnknownColor:
		return 2
	default:
		return 1
	}
}

// NewError builds a classified, located error.
func NewError(t ErrorType, trackID string, frameIndex int, offset int64, detail string) *MediaError {
	return &MediaError{Type: t, TrackID: trackID, FrameIndex: frameIndex, Offset: offset, Detail: detail}
}

// AsMediaError extracts a *MediaError from an error chain, or nil.
func AsMediaError(err error) *MediaError {
	for err != nil {
		if me, ok := err.(*MediaError); ok {
			return me
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return nil
		}
		err = u.Unwrap()
	}
	return nil
}
