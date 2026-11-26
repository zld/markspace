// Package model contains the shared microsecond time base and error type.
package model

import (
	"context"
	"errors"
	"fmt"
)

// Us is the common integer microsecond time base used by every subsystem.
type Us int64

// Code classifies failures for process exit handling.
type Code string

const (
	CodeOK       Code = ""
	CodeParse    Code = "unparseable_input"
	CodeLimit    Code = "resource_limit"
	CodeCanceled Code = "canceled"
	CodeConflict Code = "timeline_conflict"
)

// MediaError records where a media fault happened and, when relevant, the
// original and repaired timestamps.
type MediaError struct {
	Code            Code   `json:"code"`
	Source          string `json:"source,omitempty"`
	TrackID         uint32 `json:"track_id,omitempty"`
	BoxType         string `json:"box_type,omitempty"`
	BoxOffset       int64  `json:"box_offset,omitempty"`
	FirstUnreadable int64  `json:"first_unreadable_offset,omitempty"`
	OriginalTime    int64  `json:"original_time,omitempty"`
	FixedTime       int64  `json:"fixed_time,omitempty"`
	OriginalTimeSet bool   `json:"original_time_set,omitempty"`
	FixedTimeSet    bool   `json:"fixed_time_set,omitempty"`
	Err             error  `json:"-"`
}

func (e *MediaError) Error() string {
	msg := string(e.Code)
	if e.Source != "" {
		msg += " source=" + e.Source
	}
	if e.TrackID != 0 {
		msg += fmt.Sprintf(" track=%d", e.TrackID)
	}
	if e.BoxType != "" {
		msg += " box=" + e.BoxType
	}
	if e.BoxOffset >= 0 {
		msg += fmt.Sprintf(" box_offset=%d", e.BoxOffset)
	}
	if e.FirstUnreadable >= 0 {
		msg += fmt.Sprintf(" unreadable_offset=%d", e.FirstUnreadable)
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *MediaError) Unwrap() error { return e.Err }

// NewParseError constructs a parse error with source/offset provenance.
func NewParseError(source, boxType string, boxOffset, unreadable int64, err error) *MediaError {
	return &MediaError{
		Code:            CodeParse,
		Source:          source,
		BoxType:         boxType,
		BoxOffset:       boxOffset,
		FirstUnreadable: unreadable,
		Err:             err,
	}
}

// AsMediaError finds the first MediaError in err's tree.
func AsMediaError(err error) (*MediaError, bool) {
	var me *MediaError
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// ExitCode maps errors to the contract's terminal exit codes.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var me *MediaError
	if errors.As(err, &me) {
		switch me.Code {
		case CodeParse:
			return 2
		case CodeLimit, CodeCanceled:
			return 3
		}
	}
	if errors.Is(err, ErrResourceLimit) || errors.Is(err, ErrCanceled) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 3
	}
	return 2
}

// Common sentinel errors. MediaError wrappers preserve their classified code.
var (
	ErrResourceLimit = errors.New("resource limit exceeded")
	ErrCanceled      = errors.New("processing canceled")
)
