// Package plan defines the stage state machine and deterministic stage plan.
package plan

import (
	apperrors "archivecheck/internal/errors"
)

// Stage names are persisted in journals and output manifests.
type Stage string

const (
	StagePendingScan Stage = "pending_scan"
	StageScanned     Stage = "scanned"
	StagePlanned     Stage = "planned"
	StageProcessing  Stage = "processing"
	StageVerified    Stage = "verified"
	StageCompleted   Stage = "completed"
	StageFailed      Stage = "failed"
)

// PipelineStages is the happy-path order.
var PipelineStages = []Stage{
	StagePendingScan,
	StageScanned,
	StagePlanned,
	StageProcessing,
	StageVerified,
	StageCompleted,
}

// Next returns the legal happy-path successor.
func Next(s Stage) (Stage, error) {
	for i := 0; i < len(PipelineStages)-1; i++ {
		if PipelineStages[i] == s {
			return PipelineStages[i+1], nil
		}
	}
	return "", apperrors.E(apperrors.KindPlan, "no successor for terminal or unknown stage: "+string(s))
}

// CanTransition reports whether a stage migration is legal. Any non-terminal
// stage may fail. A failed record may only be explicitly reset to a fresh
// pending scan; ordinary crash recovery uses the last committed pre-failure
// stage and does not mark the process failed.
func CanTransition(from, to Stage) bool {
	if from == to {
		return false
	}
	if next, err := Next(from); err == nil && next == to {
		return true
	}
	if to == StageFailed {
		switch from {
		case StagePendingScan, StageScanned, StagePlanned, StageProcessing, StageVerified:
			return true
		default:
			return false
		}
	}
	if from == StageFailed && to == StagePendingScan {
		return true
	}
	return false
}

// Transition validates and returns a migration error classified as plan error.
func Transition(from, to Stage) error {
	if !CanTransition(from, to) {
		return apperrors.E(apperrors.KindPlan, "illegal stage transition "+string(from)+" -> "+string(to))
	}
	return nil
}

// Plan is the reusable stage plan stamped with the canonical stage version.
type Plan struct {
	Version string  `json:"version"`
	Stages  []Stage `json:"stages"`
}

// New builds a plan for the current stage version.
func New(version string) Plan {
	return Plan{Version: version, Stages: append([]Stage(nil), PipelineStages...)}
}
