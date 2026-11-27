// Package model defines the shared integer media time base, structured error
// model and processing limits used by every mediasync subsystem.
package model

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Ticks is the canonical integer time unit: nanoseconds since the media
// timeline origin. All track-local time scales are converted into Ticks
// exactly (or rejected), so no subsystem compares mixed time bases.
type Ticks int64

// Nano is the number of Ticks in one second.
const Nano Ticks = 1_000_000_000

// Milli is the number of Ticks in one millisecond.
const Milli Ticks = 1_000_000

// ErrorClass categorizes failures so callers can react programmatically
// instead of parsing error strings.
type ErrorClass string

const (
	ClassInvalidTimebase  ErrorClass = "invalid_timebase"
	ClassTimeRewind       ErrorClass = "time_rewind"
	ClassOutOfRange       ErrorClass = "out_of_range"
	ClassMissingTimestamp ErrorClass = "missing_timestamp"
	ClassCorruptBox       ErrorClass = "corrupt_box"
	ClassTruncated        ErrorClass = "truncated"
	ClassIndexCorrupt     ErrorClass = "index_corrupt"
	ClassLimitExceeded    ErrorClass = "limit_exceeded"
	ClassRTP              ErrorClass = "rtp_parse"
	ClassCheckpoint       ErrorClass = "checkpoint"
	ClassArgument         ErrorClass = "argument"
)

// MediaError is the structured error carried across every pipeline stage.
// It records the failing input, the track, the byte/event offset where the
// problem was observed and a stable machine-readable category.
type MediaError struct {
	Class   ErrorClass `json:"class"`
	Input   string     `json:"input,omitempty"`
	Track   uint32     `json:"track,omitempty"`
	Offset  int64      `json:"offset,omitempty"`
	Message string     `json:"message"`
}

// Error implements the error interface.
func (e *MediaError) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Class))
	if e.Input != "" {
		b.WriteString(" input=")
		b.WriteString(e.Input)
	}
	if e.Track != 0 {
		b.WriteString(" track=")
		b.WriteString(strconv.FormatUint(uint64(e.Track), 10))
	}
	if e.Offset >= 0 {
		b.WriteString(" offset=")
		b.WriteString(strconv.FormatInt(e.Offset, 10))
	}
	b.WriteString(": ")
	b.WriteString(e.Message)
	return b.String()
}

// NewError builds a MediaError with an unknown offset.
func NewError(class ErrorClass, format string, args ...any) *MediaError {
	return &MediaError{Class: class, Offset: -1, Message: fmt.Sprintf(format, args...)}
}

// AtError annotates a class with input/track/offset context.
func AtError(class ErrorClass, input string, track uint32, offset int64, format string, args ...any) *MediaError {
	return &MediaError{
		Class:   class,
		Input:   input,
		Track:   track,
		Offset:  offset,
		Message: fmt.Sprintf(format, args...),
	}
}

// AsMediaError unwraps err into a *MediaError or returns nil.
func AsMediaError(err error) *MediaError {
	var me *MediaError
	if errors.As(err, &me) {
		return me
	}
	return nil
}
