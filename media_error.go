package mediakernel

import (
	"errors"
	"fmt"
)

// Processing phases reported in structured errors. Keeping these as constants
// lets tests and the CLI classify failures deterministically.
const (
	PhaseParse     = "parse"     // packet/container parsing
	PhaseSequence  = "sequence"  // RTP sequence validation
	PhaseTimeline  = "timeline"  // clock mapping
	PhaseDigest    = "digest"    // content/container hashing
	PhaseSpec      = "spec"      // bitrate / alignment / quality checks
	PhaseMigrate   = "migration" // manifest staging and commit
	PhaseQueue     = "queue"     // bounded pipeline / backpressure
	PhaseCancelled = "cancelled" // context cancellation
)

// Exit codes used by the assetcheck CLI.
const (
	ExitOK           = 0 // spec passed
	ExitBusinessFail = 1 // business validation failure (spec/alignment/digest)
	ExitRuntimeError = 2 // bad arguments or runtime error (corrupt input, interrupt)
)

// MediaError is the structured error model shared by every subsystem. It
// carries the byte/frame input offset where the problem was detected, the
// processing phase and, where relevant, a media time range.
type MediaError struct {
	Phase  string     `json:"phase"`
	Reason string     `json:"reason"`
	Offset int64      `json:"offset"`
	Seq    *uint16    `json:"seq,omitempty"`
	Time   *TimeRange `json:"time_range,omitempty"`
	Cause  error      `json:"-"`
}

// Error implements error.
func (e *MediaError) Error() string {
	s := fmt.Sprintf("media error: phase=%s offset=%d reason=%s", e.Phase, e.Offset, e.Reason)
	if e.Seq != nil {
		s += fmt.Sprintf(" seq=%d", *e.Seq)
	}
	if e.Time != nil {
		s += fmt.Sprintf(" time=[%d,%d)", e.Time.StartTicks, e.Time.EndTicks)
	}
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

// Unwrap supports errors.Is/As through wrapped causes.
func (e *MediaError) Unwrap() error { return e.Cause }

// NewError builds a MediaError with the given phase, offset and reason.
func NewError(phase string, offset int64, reason string) *MediaError {
	return &MediaError{Phase: phase, Reason: reason, Offset: offset}
}

// WithSeq attaches the offending RTP sequence number.
func (e *MediaError) WithSeq(seq uint16) *MediaError {
	s := seq
	e.Seq = &s
	return e
}

// WithTime attaches the media time range involved.
func (e *MediaError) WithTime(r TimeRange) *MediaError {
	t := r
	e.Time = &t
	return e
}

// WithCause wraps a lower-level error.
func (e *MediaError) WithCause(cause error) *MediaError {
	e.Cause = cause
	return e
}

// AsMediaError extracts a *MediaError from err if one is present in its chain.
func AsMediaError(err error) (*MediaError, bool) {
	var me *MediaError
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// ErrCancelled is returned by pipeline stages when the context is cancelled.
var ErrCancelled = NewError(PhaseCancelled, -1, "pipeline cancelled")

// ClassifyExitCode maps an error to the documented CLI exit code. Business
// validation failures return 1; everything else (bad input, runtime,
// cancellation, interrupted migration) returns 2.
func ClassifyExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var me *MediaError
	if errors.As(err, &me) && me.Phase == PhaseSpec {
		return ExitBusinessFail
	}
	return ExitRuntimeError
}
