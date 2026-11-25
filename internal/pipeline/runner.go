package pipeline

import (
	"errors"
	"fmt"
	"time"

	"regress-kernel/internal/ingest"
	"regress-kernel/internal/media"
)

// ValidationError carries a failed frame report so callers can report the bad
// frame indexes and distinguish corruption from other failures (exit code 2).
type ValidationError struct {
	TaskKey string
	Report  media.FrameReport
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("task %s failed frame validation: %d faults",
		e.TaskKey, len(e.Report.Faults))
}

// IsValidation reports whether err (or a wrapped error) is a frame failure.
func IsValidation(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

// StageSample is one measured stage execution, used for P95 latency.
type StageSample struct {
	Stage Stage `json:"stage"`
	Nanos int64 `json:"nanos"`
}

// TaskResult is everything one rendition produced during a program run.
type TaskResult struct {
	TaskKey    string
	Rendition  string
	Frames     media.FrameReport
	Sync       media.SyncReport
	Records    []ArtifactRecord
	Checkpoint *Checkpoint // highest resumed marker, nil on a fresh run
	Samples    []StageSample
}

// RunOptions fully determines one program execution against a workspace.
type RunOptions struct {
	CaseName        string
	Seed            int64
	Fx              *media.Fixture
	CrashAfterWrite bool
}

// RunTask drives one rendition through ingest, align and publish, resuming
// from the highest valid checkpoint. All side effects are idempotent so a
// rerun after any crash converges on the same artifacts and state.
func (ws *Workspace) RunTask(opts RunOptions, rd media.Rendition,
	audio []media.AudioPacket, loss media.LossReport) (*TaskResult, error) {
	cfg := opts.Fx.Config
	summary, err := BuildInputSummary(rd.Frames, audio, loss.Injected)
	if err != nil {
		return nil, err
	}
	digest, err := InputDigest(summary)
	if err != nil {
		return nil, err
	}

	st, existed, err := ws.LoadState(rd.TaskKey)
	if err != nil {
		return nil, err
	}
	if !existed {
		st = &TaskState{
			TaskKey: rd.TaskKey, Rendition: rd.Config.Name,
			Status: StateRunning, InputSummary: summary,
		}
	}
	if st.Status == StateQuarantined {
		return nil, fmt.Errorf("task %s is quarantined: %s", rd.TaskKey, st.Failure)
	}
	res := &TaskResult{TaskKey: rd.TaskKey, Rendition: rd.Config.Name}
	last, err := ws.LastCheckpoint(rd.TaskKey, digest)
	if err != nil {
		return nil, err
	}
	res.Checkpoint = last
	resumeAt := 0
	if last != nil {
		resumeAt = last.StageOrdinal + 1
		if st.Status == StateCommitted && resumeAt >= len(Stages) {
			// Fully committed task: reuse artifacts, but recompute the cheap,
			// deterministic validation and alignment so rerun reports still
			// carry frame counters and sync statistics.
			res.Records = st.Artifacts
			res.Frames, _ = ingest.ValidateFrames(rd.Frames, rd.Config.Width,
				rd.Config.Height, ingest.DefaultFrameLimits())
			res.Sync = ingest.Align(rd.Frames, audio, opts.Fx.Config.Clock)
			return res, nil
		}
	}

	// Stage ingest: frame validation plus the program-level RTP loss outcome.
	// Validation is recomputed on every run so recovery reports keep their
	// frame counters and fault indexes; checkpoint writes stay stage-gated.
	{
		start := time.Now()
		rep, verr := ingest.ValidateFrames(rd.Frames, rd.Config.Width,
			rd.Config.Height, ingest.DefaultFrameLimits())
		res.Frames = rep
		res.Samples = append(res.Samples,
			StageSample{StageIngest, time.Since(start).Nanoseconds()})
		if verr != nil {
			st.Status, st.Stage, st.Faults, st.Failure =
				StateFailed, StageIngest, rep.Faults, "frame validation error"
			_ = ws.SaveState(st)
			return res, &ValidationError{TaskKey: rd.TaskKey, Report: rep}
		}
		if rep.Failed() {
			st.Status, st.Stage, st.Faults = StateFailed, StageIngest, rep.Faults
			st.Failure = "frame sequence/hash validation failed"
			_ = ws.SaveState(st)
			return res, &ValidationError{TaskKey: rd.TaskKey, Report: rep}
		}
		if resumeAt <= StageIndex(StageIngest) {
			if _, err := ws.WriteCheckpoint(rd.TaskKey, StageIngest, digest,
				map[string]interface{}{
					"frame_count":    rep.Accepted,
					"loss_injected":  loss.Injected,
					"loss_recovered": loss.Recovered,
				}); err != nil {
				return nil, err
			}
		}
	}

	// Stage align: project both tracks on the shared nanosecond clock.
	// Alignment is a pure, deterministic function, so it is recomputed on every
	// run (including recovery) to keep sync statistics present in the report;
	// only the durable checkpoint write is gated by the stage boundary.
	{
		start := time.Now()
		res.Sync = ingest.Align(rd.Frames, audio, cfg.Clock)
		res.Samples = append(res.Samples,
			StageSample{StageAlign, time.Since(start).Nanoseconds()})
		if resumeAt <= StageIndex(StageAlign) {
			if _, err := ws.WriteCheckpoint(rd.TaskKey, StageAlign, digest,
				map[string]interface{}{
					"sample_count":     res.Sync.SampleCount,
					"max_abs_error_ns": res.Sync.MaxAbsErrorNs,
					"mean_error_ns":    res.Sync.MeanErrorNs,
				}); err != nil {
				return nil, err
			}
		}
	}

	// Stage publish: idempotent fragment writes with crash-window recovery.
	// Always call Publish: when fragments already exist it verifies each
	// digest and adopts them without rewriting, so a crashed-but-durable task
	// recovers its artifact records on rerun.
	{
		start := time.Now()
		records, _, perr := ws.Publish(PublishOptions{
			TaskKey: rd.TaskKey, Rendition: rd.Config.Name,
			Frames: rd.Frames, Clock: cfg.Clock,
		})
		res.Samples = append(res.Samples,
			StageSample{StagePublish, time.Since(start).Nanoseconds()})
		if perr != nil {
			return res, perr // ErrDigestMismatch surfaces from here
		}
		res.Records = records
		st.Artifacts = records
		st.Stage = StagePublish
		st.UpdatedAt = fmt.Sprintf("%s/seed-%d/publish", opts.CaseName, opts.Seed)
		if err := ws.SaveState(st); err != nil {
			return nil, err
		}
		if _, err := ws.WriteCheckpoint(rd.TaskKey, StagePublish, digest,
			map[string]interface{}{"segments": len(records)}); err != nil {
			return nil, err
		}
	}
	return res, nil
}
