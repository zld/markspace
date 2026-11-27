package media

import (
	"errors"
	"fmt"
)

// Kind classifies failures so that callers (and the CLI) can map them to
// stable exit codes and decide whether a run may be resumed.
type Kind int

const (
	KindInternal Kind = iota
	KindTruncatedBox
	KindBadTimescale
	KindOverflow
	KindTimeRegression
	KindKeyframeMiss
	KindBudget
	KindParamConflict
	KindVerify
)

func (k Kind) String() string {
	switch k {
	case KindTruncatedBox:
		return "truncated-box"
	case KindBadTimescale:
		return "bad-timescale"
	case KindOverflow:
		return "overflow"
	case KindTimeRegression:
		return "time-regression"
	case KindKeyframeMiss:
		return "keyframe-miss"
	case KindBudget:
		return "budget"
	case KindParamConflict:
		return "param-conflict"
	case KindVerify:
		return "verify"
	default:
		return "internal"
	}
}

// Error is the shared error model. Every failure carries the operation,
// the track it concerns (0 when not track-specific), the byte offset in
// the input when known, and whether the run can be resumed after fixing
// the cause.
type Error struct {
	Kind        Kind
	Op          string
	TrackID     int
	Offset      int64
	HasOffset   bool
	Recoverable bool
	Msg         string
	Cause       error
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%s: %s", e.Op, e.Msg)
	if e.TrackID != 0 {
		s += fmt.Sprintf(" (track %d)", e.TrackID)
	}
	if e.HasOffset {
		s += fmt.Sprintf(" (offset %d)", e.Offset)
	}
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return fmt.Sprintf("%s: %s", e.Kind, s)
}

func (e *Error) Unwrap() error { return e.Cause }

// IsKind reports whether err (or any error it wraps) is a media.Error of
// the given kind.
func IsKind(err error, k Kind) bool {
	var me *Error
	if errors.As(err, &me) {
		return me.Kind == k
	}
	return false
}
