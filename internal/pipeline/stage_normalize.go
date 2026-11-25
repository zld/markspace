package pipeline

import (
	"replaycheck/internal/model"
	"replaycheck/internal/report"
	"replaycheck/internal/timeline"
)

func init() { register(model.StageNormalize, (*run).stageNormalize) }

func (r *run) stageNormalize() error {
	input := model.Fingerprint("normalize", r.ingestOut)
	var tl timeline.Timeline
	if out, ok := r.fetchIfSkipped(model.StageNormalize, "normalize", input, &tl); ok {
		r.tl = &tl
		r.normOut = out
		r.record(model.StageNormalize, input, true)
		r.emitNormalize(&tl)
		r.finish(model.StageNormalize, out)
		return nil
	}
	cached := r.fetch("normalize", input, &tl)
	if !cached {
		cont, err := r.container()
		if err != nil {
			return err
		}
		events, err := r.eventFile()
		if err != nil {
			return err
		}
		built, err := timeline.Normalize(cont, events, 0)
		if err != nil {
			return err
		}
		tl = *built
		if err := r.save("normalize", input, built); err != nil {
			return err
		}
	}
	r.tl = &tl
	r.normOut, _ = model.DigestJSON(tl)
	r.record(model.StageNormalize, input, cached)
	r.emitNormalize(&tl)
	r.finish(model.StageNormalize, r.normOut)
	return nil
}

func (r *run) emitNormalize(tl *timeline.Timeline) {
	r.result.WrapCount = tl.WrapCount
	r.result.RemainderNanos = tl.RemainderNanos
	neg := 0
	for _, tr := range tl.Tracks {
		for _, s := range tr.Samples {
			if s.End < s.Start {
				neg++
			}
		}
	}
	r.out.Emit(report.KindTimeline, map[string]any{
		"phase": "normalize", "start": tl.Start, "end": tl.End, "base": tl.Base,
		"tracks": len(tl.Tracks), "events": len(tl.Events),
		"wraps": tl.WrapCount, "remainderNanos": tl.RemainderNanos,
		"nonIntegralBoundaries": tl.NonIntegral, "negativeDurations": neg,
	})
}
