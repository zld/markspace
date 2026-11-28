// Package errors defines the stable error vocabulary shared by every media
// subsystem. Error codes are strings so they remain stable across builds and
// survive marshaling into evidence artifacts; each Error also carries the
// byte offset inside the input where applicable and the recovery state at the
// point of failure.
package errors

import (
	"fmt"
)

// Stable error codes. Consumers may switch on these strings; do not renumber
// or rename them.
const (
	// CodeTruncatedBox: a box header declares bytes past end of file.
	CodeTruncatedBox = "MP4_TRUNCATED_BOX"
	// CodeInvalidBox: a box payload cannot be parsed (bad size/version/...).
	CodeInvalidBox = "MP4_INVALID_BOX"
	// CodeSampleOutOfRange: a sample table points outside the media data.
	CodeSampleOutOfRange = "MP4_SAMPLE_OUT_OF_RANGE"
	// CodeInconsistentTable: sample tables disagree on sample counts.
	CodeInconsistentTable = "MP4_INCONSISTENT_TABLE"
	// CodeUnsupportedTrack: codec/handler type is not in the supported set.
	CodeUnsupportedTrack = "MAP_UNSUPPORTED_CODEC"
	// CodeMissingTrack: a mapping rule names a source track that is absent.
	CodeMissingTrack = "MAP_MISSING_TRACK"
	// CodeDuplicateTarget: two rules claim the same target track slot.
	CodeDuplicateTarget = "MAP_DUPLICATE_TARGET"
	// CodeRTPConflict: same SSRC+sequence number arrived with different bytes.
	CodeRTPConflict = "RTP_CONTENT_CONFLICT"
	// CodeRTPGap: sequence numbers were lost beyond the reorder window.
	CodeRTPGap = "RTP_GAP"
	// CodeBoundaryMismatch: fan-in segment edges differ beyond tolerance.
	CodeBoundaryMismatch = "FANIN_BOUNDARY_MISMATCH"
	// CodeQualityGate: pixel/loudness thresholds were not met.
	CodeQualityGate = "POLICY_QUALITY_GATE"
	// CodeBudgetExceeded: per-asset processing budget ran out.
	CodeBudgetExceeded = "POLICY_BUDGET_EXCEEDED"
	// CodeSizeLimit: no feasible representation fits the byte ceiling.
	CodeSizeLimit = "POLICY_SIZE_LIMIT"
	// CodeCorruptArtifact: an output artifact failed its summary check.
	CodeCorruptArtifact = "REMUX_CORRUPT_ARTIFACT"
)

// RecoveryState classifies how far a pipeline stage had recovered when the
// error was recorded.
type RecoveryState string

const (
	// StateNone: no recovery attempted yet.
	StateNone RecoveryState = "none"
	// StateRecoverable: processing can resume from the last keyframe.
	StateRecoverable RecoveryState = "recoverable"
	// StateRecovered: a layer actually resumed after a gap.
	StateRecovered RecoveryState = "recovered"
	// StateFatal: the asset cannot be completed.
	StateFatal RecoveryState = "fatal"
)

// Error is the structured error type propagated across modules.
type Error struct {
	// Code is one of the stable Code* constants.
	Code string
	// Message is a human-readable explanation, never parsed by callers.
	Message string
	// Offset is the byte offset inside the input that triggered the error
	// (zero when not applicable, e.g. policy decisions).
	Offset int64
	// RawTimestamp is the original media timestamp (source timescale ticks)
	// associated with the failure; RawTimescale gives its time base.
	RawTimestamp int64
	RawTimescale uint32
	// State records recovery status at failure time.
	State RecoveryState
	// Cause wraps an underlying library error, if any.
	Cause error
}

func (e *Error) Error() string {
	s := e.Code
	if e.Offset > 0 {
		s += fmt.Sprintf(" @offset=%d", e.Offset)
	}
	if e.RawTimescale > 0 {
		s += fmt.Sprintf(" @tick=%d/%dHz", e.RawTimestamp, e.RawTimescale)
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

// Unwrap exposes the wrapped cause for errors.Is/errors.As.
func (e *Error) Unwrap() error { return e.Cause }

// New builds an Error with the given code and message; state defaults to
// StateNone.
func New(code, message string) *Error {
	return &Error{Code: code, Message: message, State: StateNone}
}

// AtOffset attaches a byte offset and returns the same error.
func (e *Error) AtOffset(off int64) *Error {
	e.Offset = off
	return e
}

// AtTick attaches the original timestamp and its timescale and returns the
// same error.
func (e *Error) AtTick(tick int64, timescale uint32) *Error {
	e.RawTimestamp = tick
	e.RawTimescale = timescale
	return e
}

// WithState attaches a recovery state and returns the same error.
func (e *Error) WithState(s RecoveryState) *Error {
	e.State = s
	return e
}

// Wrap attaches a cause and returns the same error.
func (e *Error) Wrap(cause error) *Error {
	e.Cause = cause
	return e
}

// As extracts a *Error from any wrapped error chain.
func As(err error) (*Error, bool) {
	if err == nil {
		return nil, false
	}
	var me *Error
	if asImpl(err, &me) {
		return me, true
	}
	return nil, false
}

func asImpl(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		type wrapper interface{ Unwrap() error }
		w, ok := err.(wrapper)
		if !ok {
			return false
		}
		err = w.Unwrap()
	}
	return false
}
