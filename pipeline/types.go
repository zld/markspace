// Package pipeline orchestrates the demux -> normalize -> segment -> manifest
// stages in dependency order. A failed stage never commits partial state:
// checkpoints are written only after a stage's artifacts pass validation.
package pipeline

import (
	"fmt"

	"mediadist/normalize"
	"mediadist/segment"
	"mediadist/timeline"
)

// Stage names in execution order.
const (
	StageDemux     = timeline.StageDemux
	StageTimeline  = timeline.StageTimeline
	StageNormalize = timeline.StageNormalize
	StageSegment   = timeline.StageSegment
	StageManifest  = timeline.StageManifest
)

// OrderedStages is the dependency order the runner executes. Validation faults
// surface on the timeline stage, which runs immediately after demux.
var OrderedStages = []string{StageDemux, StageNormalize, StageSegment, StageManifest}

// Options controls one pipeline run.
type Options struct {
	// OutputDir is the only directory the pipeline writes to.
	OutputDir string
	// TargetAudioRate is the normalized sample rate (0 => 48 kHz).
	TargetAudioRate uint32
	// ColorPriority lists color sources highest-priority first.
	ColorPriority []timeline.ColorSource
	// CLIColor forces a color description when non-nil.
	CLIColor *timeline.ColorDesc
	// Segment/bitrate conformance configuration.
	Segment segment.Config
}

// StageState records whether a stage completed and what it produced.
type StageState struct {
	Name      string `json:"name"`
	Completed bool   `json:"completed"`
	// Failed is set when the stage terminated with an error; reruns resume here.
	Failed bool `json:"failed,omitempty"`
	// ErrorCode and ErrorText preserve the localized failure for diagnostics.
	ErrorCode string `json:"error_code,omitempty"`
	ErrorText string `json:"error_text,omitempty"`
}

// RunError is the unified pipeline error. It always carries the failing stage
// and, when available, the track locator from the underlying LocError.
type RunError struct {
	Stage   string
	TrackID uint32
	Code    string
	Err     error
}

func (e *RunError) Error() string {
	loc := "track=-"
	if e.TrackID != 0 {
		loc = fmt.Sprintf("track=%d", e.TrackID)
	}
	// When the cause is itself a LocError, render its operation and cause
	// rather than re-printing its code/stage, so the message is not doubled.
	var le *timeline.LocError
	if asLoc(e.Err, &le) {
		if le.Err != nil {
			return fmt.Sprintf("%s: stage=%s %s op=%s: %v", e.Code, e.Stage, loc, le.Op, le.Err)
		}
		return fmt.Sprintf("%s: stage=%s %s op=%s", e.Code, e.Stage, loc, le.Op)
	}
	return fmt.Sprintf("%s: stage=%s %s: %v", e.Code, e.Stage, loc, e.Err)
}

func (e *RunError) Unwrap() error { return e.Err }

// asRunError converts any error into a RunError bound to a stage, preserving
// an embedded timeline.LocError's code and track when present.
func asRunError(stage string, err error) *RunError {
	re := &RunError{Stage: stage, Code: "INTERNAL", Err: err}
	var le *timeline.LocError
	if asLoc(err, &le) {
		re.Code = le.Code
		re.TrackID = le.TrackID
		if le.Stage != "" {
			re.Stage = le.Stage
		}
	}
	return re
}

// NewRunErrorForTest builds a RunError with an explicit code. It is used by the
// CLI to classify usage and I/O errors under the same exit-code contract.
func NewRunErrorForTest(code string, err error) *RunError {
	return &RunError{Stage: StageManifest, Code: code, Err: err}
}

// asLoc walks the error chain for a *timeline.LocError without importing
// errors.As repeatedly.
func asLoc(err error, target **timeline.LocError) bool {
	for err != nil {
		if le, ok := err.(*timeline.LocError); ok {
			*target = le
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// keep normalize referenced for future stage result typing.
var _ = normalize.DefaultTargetRate
