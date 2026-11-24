// Package media holds the shared time base, error model and container probe.
package media

import (
	"errors"
	"fmt"
	"strings"
)

// Kind classifies a media-processing problem. Categories are stable strings
// because they are written to reports and provenance records.
type Kind string

const (
	KindTruncatedInput  Kind = "truncated_input"
	KindCorruptBox      Kind = "corrupt_box"
	KindZeroDuration    Kind = "zero_duration_track"
	KindWraparound      Kind = "timestamp_wraparound"
	KindNonMonotonic    Kind = "non_monotonic_timestamp"
	KindTimestampGap    Kind = "timestamp_gap"
	KindMissingKeyframe Kind = "missing_keyframe"
	KindSyncTolerance   Kind = "av_sync_out_of_tolerance"
	KindRTPGap          Kind = "rtp_unrecoverable_gap"
	KindCheckpoint      Kind = "checkpoint_invalid"
	KindLimit           Kind = "limit_exceeded"
	KindIO              Kind = "io_error"
	KindUnsupported     Kind = "unsupported_input"
	KindInternal        Kind = "internal_error"
	KindArgument        Kind = "invalid_argument"
)

// Severity says whether an issue fails the owning input or is only recorded.
type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Issue is a classified, non-fatal observation attached to an index, a
// segment or a provenance record.
type Issue struct {
	Kind     Kind     `json:"kind"`
	Severity Severity `json:"severity"`
	Detail   string   `json:"detail"`
	At       int      `json:"at,omitempty"` // sample index, when applicable
	TimeUS   int64    `json:"time_us"`
}

// Error is the classified error used across all subsystems.
type Error struct {
	Kind   Kind   `json:"kind"`
	File   string `json:"file,omitempty"`
	Detail string `json:"detail,omitempty"`
	Err    error  `json:"-"`
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Kind))
	if e.File != "" {
		b.WriteString(": ")
		b.WriteString(e.File)
	}
	if e.Detail != "" {
		b.WriteString(": ")
		b.WriteString(e.Detail)
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// NewError builds a classified error.
func NewError(kind Kind, file, format string, args ...any) *Error {
	return &Error{Kind: kind, File: file, Detail: fmt.Sprintf(format, args...)}
}

// WrapError attaches a category while preserving the underlying error.
func WrapError(kind Kind, file string, err error) *Error {
	if err == nil {
		return nil
	}
	var me *Error
	if errors.As(err, &me) {
		return me
	}
	return &Error{Kind: kind, File: file, Err: err}
}

// AsError extracts a classified error; unknown errors become internal errors.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var me *Error
	if errors.As(err, &me) {
		return me
	}
	return &Error{Kind: KindInternal, Err: err}
}

// IsKind reports whether err belongs to the given category.
func IsKind(err error, kind Kind) bool {
	var me *Error
	return errors.As(err, &me) && me.Kind == kind
}

// Category returns a stable category string for any error.
func Category(err error) string {
	if err == nil {
		return ""
	}
	return string(AsError(err).Kind)
}
