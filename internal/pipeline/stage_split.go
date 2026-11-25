package pipeline

import (
	"replaycheck/internal/model"
	"replaycheck/internal/report"
	"replaycheck/internal/split"
	"replaycheck/internal/timeline"
)

func init() { register(model.StageSplit, (*run).stageSplit) }

func (r *run) stageSplit() error {
	opts := timeline.DefaultValidation()
	optsDigest, err := model.DigestJSON(opts)
	if err != nil {
		return model.WrapError(model.StageSplit, "options", model.TrackRef{}, 0, "cannot digest validation options", err)
	}
	input := model.Fingerprint("split", r.normOut, optsDigest)
	var plan split.Plan
	if out, ok := r.fetchIfSkipped(model.StageSplit, "split", input, &plan); ok {
		r.plan = &plan
		r.splitOut = out
		r.record(model.StageSplit, input, true)
		r.emitSplitReport()
		r.finish(model.StageSplit, out)
		return nil
	}
	cached := r.fetch("split", input, &plan)
	if !cached {
		if r.tl == nil {
			if err := r.stageNormalize(); err != nil {
				return err
			}
		}
		cuts, err := timeline.ValidateCuts(r.tl, opts)
		if err != nil {
			return err
		}
		trans, err := timeline.BuildTransitions(cuts, opts)
		if err != nil {
			return err
		}
		built, err := split.SplitCuts(r.tl, cuts, trans)
		if err != nil {
			return err
		}
		plan = *built
		if err := r.save("split", input, built); err != nil {
			return err
		}
	}
	r.plan = &plan
	if _, err := split.VerifyTransitions(r.plan); err != nil {
		return err
	}
	r.splitOut, _ = model.DigestJSON(plan)
	r.record(model.StageSplit, input, cached)
	r.emitSplitReport()
	r.finish(model.StageSplit, r.splitOut)
	return nil
}

// segmentView is the terminal-facing projection of a segment. Byte ranges stay
// inside the pipeline; only their count and total are reported, which keeps
// output bounded no matter how many samples a segment holds.
type segmentView struct {
	Index         int    `json:"index"`
	Start         int64  `json:"startTick"`
	End           int64  `json:"endTick"`
	Samples       int    `json:"sampleCount"`
	SourceBytes   uint64 `json:"sourceBytes"`
	StartTrigger  string `json:"startTrigger"`
	EndTrigger    string `json:"endTrigger"`
	TransitionIn  string `json:"transitionIn,omitempty"`
	TransitionOut string `json:"transitionOut,omitempty"`
}

func (r *run) emitSplitReport() {
	if r.plan == nil {
		return
	}
	trep, err := split.VerifyTransitions(r.plan)
	if err != nil {
		return
	}
	views := make([]segmentView, 0, len(r.plan.Segments))
	for _, s := range r.plan.Segments {
		views = append(views, segmentView{
			Index: s.Index, Start: int64(s.Span.Start), End: int64(s.Span.End),
			Samples: s.Samples, SourceBytes: s.Bytes,
			StartTrigger: s.StartName, EndTrigger: s.EndName,
			TransitionIn: s.TransitionIn, TransitionOut: s.TransitionOut,
		})
	}
	r.out.Emit(report.KindSplit, map[string]any{
		"segments": views, "transitions": trep,
		"halfOpen": true, "intervalRule": "[start,end)",
	})
}
