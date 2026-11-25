package job

import (
	"fmt"

	"replaycheck/internal/model"
)

// Runner executes ordered phases and records each transition. A phase whose
// input digest matches the last successful run is skipped and reported as a
// cache hit, which is what makes resume cheap without risking stale state.
type Runner struct {
	State       *State
	Resume      bool
	Transitions int
	Skips       int
}

// CanResume reports whether the caller asked to resume and a prior successful
// phase exists for the same sources.
func (r *Runner) CanResume(sourceSum, eventSum string) bool {
	if !r.Resume || r.State == nil {
		return false
	}
	return r.State.SourceSum == sourceSum && r.State.EventSum == eventSum
}

// ResumeAt returns the stage to resume from: the phase after the last success,
// or ingest for a fresh job. A source digest change always restarts at ingest
// so nothing from a different source can be reused.
func (r *Runner) ResumeAt(sourceSum, eventSum string) model.Stage {
	if r.State == nil || r.State.SourceSum != sourceSum || r.State.EventSum != eventSum {
		return model.StageIngest
	}
	if r.State.Complete {
		return model.StageComplete
	}
	if r.State.LastOK.Index() < 0 {
		return model.StageIngest
	}
	return r.State.LastOK.Next()
}

// Skipped reports whether the stage is before the resume point and may be
// replayed from cached digests instead of recomputed.
func (r *Runner) Skipped(stage model.Stage, resumeAt model.Stage) bool {
	return r.Resume && stage.Index() >= 0 && resumeAt.Index() >= 0 && stage.Index() < resumeAt.Index()
}

// Begin records a successful transition into stage. InputSum is the digest of
// everything the stage consumed; OutputSum the digest of what it produced.
func (r *Runner) Begin(stage model.Stage, inputSum string, cached bool, resumeFrom model.Stage) {
	if r.State == nil {
		r.State = NewState("", "", "")
	}
	for i := range r.State.Records {
		if r.State.Records[i].Stage == stage {
			r.State.Records[i].Passes++
			r.State.Records[i].InputSum = inputSum
			if cached && !r.State.Records[i].Cached {
				r.State.Records[i].Cached = true
				r.Skips++
			}
			return
		}
	}
	r.State.Records = append(r.State.Records, Record{Stage: stage, InputSum: inputSum, Passes: 1, Cached: cached, ResumeFrom: resumeFrom})
	r.Transitions++
	if cached {
		r.Skips++
	}
}

// Complete marks stage successful with its output digest.
func (r *Runner) Complete(stage model.Stage, outputSum string) {
	for i := range r.State.Records {
		if r.State.Records[i].Stage == stage {
			r.State.Records[i].OutputSum = outputSum
			break
		}
	}
	if stage.Index() > r.State.LastOK.Index() {
		r.State.LastOK = stage
	}
	if stage == model.StageComplete {
		r.State.Complete = true
	}
	r.State.FailedAt = ""
	r.State.Failure = ""
}

// Fail records the failing stage and its recovery point. The last success is
// preserved so a rerun resumes exactly there.
func (r *Runner) Fail(stage model.Stage, offset uint64, err error) {
	if r.State == nil {
		r.State = NewState("", "", "")
	}
	r.State.FailedAt = stage
	r.State.Failure = err.Error()
	r.State.FailureOff = offset
}

// Record returns the record for a stage.
func (r *Runner) Record(stage model.Stage) (Record, bool) {
	if r.State == nil {
		return Record{}, false
	}
	for _, rec := range r.State.Records {
		if rec.Stage == stage {
			return rec, true
		}
	}
	return Record{}, false
}

// Summary renders stage transitions for the terminal report.
func (r *Runner) Summary() []string {
	var out []string
	for _, rec := range r.State.Records {
		status := "fresh"
		if rec.Cached {
			status = "cached"
		}
		out = append(out, fmt.Sprintf("%s %s in=%s out=%s", rec.Stage, status, short(rec.InputSum), short(rec.OutputSum)))
	}
	return out
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
