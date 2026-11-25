package split

import (
	"replaycheck/internal/model"
	"replaycheck/internal/timeline"
)

// TransitionReport summarizes cross-fade consistency for the split stage.
type TransitionReport struct {
	Count      int             `json:"count"`
	Boundaries int             `json:"boundaries"`
	Checked    int             `json:"checked"`
	OverlapUs  []timeline.Tick `json:"overlapUs"`
}

// VerifyTransitions enforces the two invariants the downstream renderer needs:
// every interior boundary has exactly one window, and both neighbours agree on
// that window's width. Endpoint cuts must not carry an overlap.
func VerifyTransitions(p *Plan) (TransitionReport, error) {
	rep := TransitionReport{Boundaries: len(p.Segments) + 1, Count: len(p.Transitions)}
	if len(p.Transitions) != len(p.Segments)+1 {
		return rep, model.Errorf(model.StageSplit, "transition", model.TrackRef{}, 0,
			"want %d boundary markers for %d segments, got %d", len(p.Segments)+1, len(p.Segments), len(p.Transitions))
	}
	overlaps := make([]timeline.Tick, 0, len(p.Segments))
	for i, tr := range p.Transitions {
		interior := i > 0 && i < len(p.Transitions)-1
		if !interior {
			if tr.OverlapUs != 0 {
				return rep, model.Errorf(model.StageSplit, "transition", model.TrackRef{}, uint64(tr.CutTick),
					"boundary %d at timeline edge carries overlap %d", i, tr.OverlapUs)
			}
			continue
		}
		if tr.OverlapUs <= 0 {
			return rep, model.Errorf(model.StageSplit, "transition", model.TrackRef{}, uint64(tr.CutTick),
				"interior boundary %d at tick %d has no cross-fade", i, tr.CutTick)
		}
		left := p.Segments[i-1]
		right := p.Segments[i]
		if left.TransitionOut != tr.Name || right.TransitionIn != tr.Name {
			return rep, model.Errorf(model.StageSplit, "transition", model.TrackRef{}, uint64(tr.CutTick),
				"boundary %d cross-fade not mirrored between segments %d and %d", i, i-1, i)
		}
		// The window must sit entirely inside the two neighbours so the
		// renderer can read both sides without truncating either clip.
		if tr.StartTick < left.Span.Start || tr.EndTick > right.Span.End {
			return rep, model.Errorf(model.StageSplit, "transition", model.TrackRef{}, uint64(tr.CutTick),
				"cross-fade [%d,%d) escapes segments [%d,%d)", tr.StartTick, tr.EndTick, left.Span.Start, right.Span.End)
		}
		overlaps = append(overlaps, tr.OverlapUs)
		rep.Checked++
	}
	rep.OverlapUs = overlaps
	return rep, nil
}
