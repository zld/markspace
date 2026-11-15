// Package model defines the transcoding task, its segments and the
// queued/running/succeeded/failed/interrupted state machine.
package model

import (
	"errors"
	"fmt"
)

// Status is the lifecycle state of a single segment.
type Status string

const (
	StatusQueued      Status = "queued"
	StatusRunning     Status = "running"
	StatusSucceeded   Status = "succeeded"
	StatusFailed      Status = "failed"
	StatusInterrupted Status = "interrupted"
)

// allowedTransitions enumerates every legal state-machine move.
var allowedTransitions = map[Status]map[Status]bool{
	StatusQueued: {
		StatusRunning:     true,
		StatusInterrupted: true,
	},
	StatusRunning: {
		StatusSucceeded:   true,
		StatusFailed:      true,
		StatusInterrupted: true,
	},
	StatusSucceeded: {
		// A succeeded segment may only be re-queued after its checkpoint
		// was invalidated (fingerprint mismatch); that reset goes through
		// ResetToQueued rather than a generic transition.
		StatusQueued: true,
	},
	StatusFailed: {
		StatusQueued:      true,
		StatusInterrupted: true,
	},
	StatusInterrupted: {
		StatusQueued: true,
	},
}

// ErrIllegalTransition is returned when a state move is not permitted.
var ErrIllegalTransition = errors.New("illegal status transition")

// Transition validates and applies a status change.
func (s *Status) Transition(next Status) error {
	if !allowedTransitions[*s][next] {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, *s, next)
	}
	*s = next
	return nil
}

// CanTransition reports whether the move is legal.
func (s Status) CanTransition(next Status) bool {
	return allowedTransitions[s][next]
}

// EncodeResult records the verified encoder output for one segment.
type EncodeResult struct {
	OutputPath  string  `json:"output_path"`
	Bytes       int64   `json:"bytes"`
	ActualKbps  float64 `json:"actual_kbps"`
	TargetKbps  float64 `json:"target_kbps"`
	Deviation   float64 `json:"deviation_pct"`
	Attempts    int     `json:"attempts"`
	ExitCode    int     `json:"exit_code"`
	FailureNote string  `json:"failure_note,omitempty"`
}

// QualityResult holds the per-segment PSNR summary. Frame-level samples are
// kept in memory during the run but are not persisted in the checkpoint.
type QualityResult struct {
	FrameCount    int     `json:"frame_count"`
	MeanPSNRDB    float64 `json:"mean_psnr_db"`
	MinPSNRDB     float64 `json:"min_psnr_db"`
	MaxPSNRDB     float64 `json:"max_psnr_db"`
	IdenticalRows int     `json:"identical_frame_pairs"`
	BlackFrames   int     `json:"black_frames"`
}

// Segment is one independently schedulable chunk of the input.
type Segment struct {
	Index       int            `json:"index"`
	StartSec    float64        `json:"start_sec"`
	DurationSec float64        `json:"duration_sec"`
	Complexity  float64        `json:"complexity"`
	TargetKbps  float64        `json:"target_kbps"`
	Feasible    bool           `json:"feasible"`
	Infeasible  string         `json:"infeasible_reason,omitempty"`
	Status      Status         `json:"status"`
	Attempts    int            `json:"attempts"`
	Encode      *EncodeResult  `json:"encode,omitempty"`
	Quality     *QualityResult `json:"quality,omitempty"`
	FailureNote string         `json:"failure_note,omitempty"`
}

// Task is the full transcoding job.
type Task struct {
	ID               string     `json:"id"`
	InputPath        string     `json:"input_path"`
	InputFingerprint string     `json:"input_fingerprint"`
	DurationSec      float64    `json:"duration_sec"`
	SegmentSec       float64    `json:"segment_seconds"`
	TargetKbps       float64    `json:"target_kbps"`
	MinKbps          float64    `json:"min_kbps"`
	Segments         []*Segment `json:"segments"`
}

// ResetToQueued moves a terminal/interrupted segment back to the queue.
// Succeeded segments are only reset after explicit checkpoint invalidation.
func (seg *Segment) ResetToQueued() error {
	switch seg.Status {
	case StatusSucceeded, StatusFailed, StatusInterrupted:
		seg.Status = StatusQueued
		seg.FailureNote = ""
		return nil
	case StatusQueued:
		return nil
	default:
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, seg.Status, StatusQueued)
	}
}

// MarkInterrupted is safe to call from any state; running/queued work moves
// to interrupted while terminal states are left untouched.
func (seg *Segment) MarkInterrupted() {
	if seg.Status == StatusRunning || seg.Status == StatusQueued {
		seg.Status = StatusInterrupted
	}
}

// SucceededCount and FailedCount summarize terminal progress.
func (t *Task) SucceededCount() int {
	n := 0
	for _, seg := range t.Segments {
		if seg.Status == StatusSucceeded {
			n++
		}
	}
	return n
}

// FailedCount counts segments that ended in failure.
func (t *Task) FailedCount() int {
	n := 0
	for _, seg := range t.Segments {
		if seg.Status == StatusFailed {
			n++
		}
	}
	return n
}
