package state

import (
	"errors"
	"fmt"

	"transcode/internal/media"
	"transcode/internal/model"
)

// ErrUnrecoverable marks a checkpoint that cannot be reconciled with the
// current run (parameter drift, wrong segment count, ...). The CLI maps it
// to exit code 4.
var ErrUnrecoverable = errors.New("checkpoint cannot be recovered")

// RecoveryOutcome explains what recovery did, for terminal/JSON reporting.
type RecoveryOutcome struct {
	Resumed         bool
	Invalidated     []int // 1-based segment numbers re-queued due to fingerprint drift
	RequeuedRunning []int // segments left mid-flight by a killed process
	RequeuedFailed  []int // failed segments made retryable
}

// Recover reconciles a loaded checkpoint with the freshly planned task.
// Succeeded segments survive only when the input fingerprint matches;
// anything left running when the previous process died is re-queued, and
// failed/interrupted segments become eligible for retry. A succeeded
// segment whose output file no longer exists is also invalidated.
func Recover(cp *Checkpoint, task *model.Task, inputPath string) (*RecoveryOutcome, error) {
	out := &RecoveryOutcome{}
	if cp == nil {
		return out, nil
	}
	if cp.JobID != task.ID || len(cp.Segments) != len(task.Segments) {
		return nil, fmt.Errorf("%w: job id or segment count changed", ErrUnrecoverable)
	}
	if !paramsCompatible(cp.Params, task) {
		return nil, fmt.Errorf("%w: run parameters differ from the checkpoint", ErrUnrecoverable)
	}

	currentFP, err := media.Fingerprint(inputPath)
	if err != nil {
		return nil, fmt.Errorf("fingerprint input: %w", err)
	}
	inputChanged := cp.InputFingerprint != currentFP

	for i, saved := range cp.Segments {
		seg := task.Segments[i]
		switch saved.Status {
		case model.StatusSucceeded:
			// A verified result survives only when both the input and the
			// persisted output file still match.
			if inputChanged || outputMissing(saved) {
				invalidate(seg, saved, out)
				continue
			}
			seg.Status = model.StatusSucceeded
			seg.Encode = saved.Encode
			seg.Quality = saved.Quality
			seg.Attempts = saved.Attempts
			out.Resumed = true
		case model.StatusRunning, model.StatusQueued, model.StatusInterrupted:
			if err := seg.ResetToQueued(); err != nil {
				return nil, err
			}
			// Both "running when the process died" and the explicitly
			// interrupted state represent in-flight work being re-queued.
			if saved.Status != model.StatusQueued {
				out.RequeuedRunning = append(out.RequeuedRunning, i+1)
			}
		case model.StatusFailed:
			// Infeasibility is a terminal planning verdict, not a transient
			// encoder failure: keep such segments failed instead of retrying.
			if !seg.Feasible {
				seg.Status = model.StatusFailed
				seg.FailureNote = seg.Infeasible
				continue
			}
			if err := seg.ResetToQueued(); err != nil {
				return nil, err
			}
			out.RequeuedFailed = append(out.RequeuedFailed, i+1)
		}
	}
	return out, nil
}

func invalidate(seg, saved *model.Segment, out *RecoveryOutcome) {
	seg.Status = model.StatusQueued
	seg.Encode = nil
	seg.Quality = nil
	seg.Attempts = saved.Attempts // preserve attempt history for observability
	seg.FailureNote = "checkpoint invalidated: input or output fingerprint mismatch"
	out.Invalidated = append(out.Invalidated, seg.Index+1)
}

func outputMissing(seg *model.Segment) bool {
	if seg.Encode == nil || seg.Encode.OutputPath == "" {
		return true
	}
	if _, err := media.Fingerprint(seg.Encode.OutputPath); err != nil {
		return true
	}
	return false
}

// paramsCompatible compares only the parameters that determine encoded
// output. Concurrency (Workers) and the per-segment size ceiling are
// scheduling/resource settings: changing them must not invalidate verified
// segments, so they are deliberately excluded from this comparison.
func paramsCompatible(p Params, t *model.Task) bool {
	return p.InputPath == t.InputPath &&
		p.DurationSec == t.DurationSec &&
		p.SegmentSec == t.SegmentSec &&
		p.TargetKbps == t.TargetKbps &&
		p.MinKbps == t.MinKbps
}
