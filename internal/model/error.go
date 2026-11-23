// Package model defines the cross-module error model and shared value types.
// All packages in mediacheck classify failures through the kinds declared here
// so that probe results, manifest alignment and the transaction journal speak
// the same vocabulary.
package model

import (
	"errors"
	"fmt"
)

// FaultKind enumerates the deterministic, reproducible fault categories.
type FaultKind string

const (
	FaultNone          FaultKind = "none"
	FaultTimescale     FaultKind = "timescale"     // zero/invalid timescale or overflow
	FaultClockSkew     FaultKind = "clock_skew"    // node clock drift
	FaultLate          FaultKind = "late"          // segment arrived after deadline
	FaultDuplicate     FaultKind = "duplicate"     // same segment number seen twice
	FaultTruncate      FaultKind = "truncated"     // segment cut short
	FaultKeyframe      FaultKind = "keyframe_gap"  // keyframe interval exceeded
	FaultBackwards     FaultKind = "ts_backwards"  // timestamps moved backwards
	FaultMetaConflict  FaultKind = "metadata"      // channel/rate/color conflict
	FaultDiscontinuity FaultKind = "discontinuity" // HLS discontinuity reset
)

// ParseFaultKind converts a CLI token into a FaultKind.
func ParseFaultKind(s string) (FaultKind, error) {
	switch FaultKind(s) {
	case FaultNone, FaultTimescale, FaultClockSkew, FaultLate, FaultDuplicate,
		FaultTruncate, FaultKeyframe, FaultBackwards, FaultMetaConflict, FaultDiscontinuity:
		return FaultKind(s), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownFault, s)
	}
}

// Stage identifies a transaction pipeline stage.
type Stage string

const (
	StageParse     Stage = "parse"
	StageProbe     Stage = "probe"
	StageNormalize Stage = "normalize"
	StageValidate  Stage = "validate"
	StageCommit    Stage = "commit"
)

// Issue is one classified problem bound to a representation and a timeline
// interval expressed in the unified integer time base.
type Issue struct {
	Kind           FaultKind `json:"kind"`
	Representation string    `json:"representation"`
	StartTick      int64     `json:"start_tick"`
	EndTick        int64     `json:"end_tick"`
	Detail         string    `json:"detail"`
}

// Range is a half-open integer interval [Start, End).
type Range struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Valid reports whether r is well formed.
func (r Range) Valid() bool { return r.Start >= 0 && r.End > r.Start }

// Overlaps reports whether two half-open ranges intersect.
func (r Range) Overlaps(o Range) bool {
	return r.Valid() && o.Valid() && r.Start < o.End && o.Start < r.End
}

// Sentinel errors shared across modules.
var (
	ErrZeroTimescale  = errors.New("timescale must be greater than zero")
	ErrOverflow       = errors.New("integer overflow while converting time values")
	ErrLossyTime      = errors.New("time value cannot be represented losslessly")
	ErrDurationBounds = errors.New("duration outside allowed bounds")
	ErrUnsafePath     = errors.New("unsafe or non-contained path rejected")
	ErrMemoryLimit    = errors.New("parse memory limit exceeded")
	ErrReportTooLarge = errors.New("report exceeds maximum size")
	ErrUnknownFault   = errors.New("unknown fault kind")
	ErrStageFailed    = errors.New("transaction stage failed")
	ErrCorruptJournal = errors.New("write-ahead journal is corrupt")
	ErrTruncated      = errors.New("media segment is truncated")
)

// StageError wraps a failure with the pipeline stage that produced it.
type StageError struct {
	Stage Stage
	Err   error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("stage %s: %v", e.Stage, e.Err)
}

// Is makes every StageError satisfy errors.Is(err, ErrStageFailed) while the
// wrapped cause remains reachable through Unwrap.
func (e *StageError) Is(target error) bool {
	return target == ErrStageFailed
}

func (e *StageError) Unwrap() error { return e.Err }

// NewStageError builds a StageError, joining the cause.
func NewStageError(stage Stage, err error) *StageError {
	return &StageError{Stage: stage, Err: err}
}
