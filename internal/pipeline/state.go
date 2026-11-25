// Package pipeline orchestrates the fixed stage order scan -> tracks ->
// recover -> trace -> segment -> verify. State only moves forward, every
// failure records its stage, input offset and category, and a completed
// checkpoint is never overwritten by a later failure.
package pipeline

import (
	"tracecheck/internal/clock"
)

// Stage is one processing phase in its canonical order.
type Stage string

const (
	StageScan     Stage = "scan"
	StageTracks   Stage = "tracks"
	StageRecover  Stage = "recover"
	StageTrace    Stage = "trace"
	StageSegment  Stage = "segment"
	StageVerify   Stage = "verify"
)

// orderedStages is the only legal progression.
var orderedStages = []Stage{
	StageScan, StageTracks, StageRecover, StageTrace, StageSegment, StageVerify,
}

// StageFailure pins a failure to its stage and original input byte offset.
type StageFailure struct {
	Stage    Stage          `json:"stage"`
	Offset   clock.Offset   `json:"offset"`
	Category clock.Category `json:"category"`
	Detail   string         `json:"detail"`
}

// State is the mutable pipeline state machine.
type State struct {
	current  Stage
	finished map[Stage]bool
	transitions int
	Failure  *StageFailure
}

// NewState starts before the first stage.
func NewState() *State {
	return &State{finished: map[Stage]bool{}}
}

// Advance moves from the current stage to next, refusing backwards or
// out-of-order movement. Finished stages stay finished.
func (s *State) Advance(next Stage) error {
	want := Stage("")
	if s.current == "" {
		want = orderedStages[0]
	} else {
		idx := indexOf(s.current)
		if idx < 0 || idx+1 >= len(orderedStages) {
			return &clock.ErrStageOrder{From: string(s.current), To: string(next)}
		}
		want = orderedStages[idx+1]
	}
	if next != want {
		return &clock.ErrStageOrder{From: string(s.current), To: string(next)}
	}
	s.current = next
	s.transitions++
	return nil
}

// Complete marks the current stage finished without changing the order.
func (s *State) Complete(stage Stage) {
	s.finished[stage] = true
}

// IsFinished reports whether a stage already produced its checkpoint.
func (s *State) IsFinished(stage Stage) bool { return s.finished[stage] }

// Current returns the active stage, or "" before the first advance.
func (s *State) Current() Stage { return s.current }

// Transitions counts successful forward moves, reported by the demo.
func (s *State) Transitions() int { return s.transitions }

// Fail records the first failure for a stage. Existing completed checkpoints
// are preserved and never overwritten.
func (s *State) Fail(stage Stage, off clock.Offset, cat clock.Category, detail string) {
	if s.Failure != nil {
		return
	}
	s.Failure = &StageFailure{
		Stage: stage, Offset: off, Category: cat, Detail: detail,
	}
}

func indexOf(stage Stage) int {
	for i, s := range orderedStages {
		if s == stage {
			return i
		}
	}
	return -1
}

// AllStages exposes the canonical order to the CLI and tests.
func AllStages() []Stage {
	out := make([]Stage, len(orderedStages))
	copy(out, orderedStages)
	return out
}
