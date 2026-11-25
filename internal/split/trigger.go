// Package split turns validated cut points into derived segment descriptors.
// Segment intervals are half-open [start,end): a tick belongs to exactly one
// segment, so no frame is duplicated or lost at a boundary.
package split

import (
	"fmt"

	"replaycheck/internal/model"
	"replaycheck/internal/timeline"
)

// Segment is one half-open slice of the replay timeline.
type Segment struct {
	Index      int           `json:"index"`
	Span       timeline.Span `json:"span"`
	StartEvent int           `json:"startEvent"`
	EndEvent   int           `json:"endEvent"`
	StartName  string        `json:"startTrigger"`
	EndName    string        `json:"endTrigger"`
	// TransitionIn/Out name the cross-fade windows attached at each edge,
	// or "" when the edge is a hard cut at the timeline boundary.
	TransitionIn  string `json:"transitionIn"`
	TransitionOut string `json:"transitionOut"`
	Samples       int    `json:"sampleCount"`
	Bytes         uint64 `json:"sourceBytes"`
}

// Plan is the split stage output.
type Plan struct {
	Segments    []Segment             `json:"segments"`
	Transitions []timeline.Transition `json:"transitions"`
	TimelineEnd timeline.Tick         `json:"timelineEnd"`
}

// SplitCuts builds segments from strictly increasing cut points. The first cut
// opens segment 0 and the last cut closes the final segment, matching the
// closed-open interval rule.
func SplitCuts(tl *timeline.Timeline, cuts []timeline.ClipPoint, trans []timeline.Transition) (*Plan, error) {
	if len(cuts) < 2 {
		return nil, model.NewError(model.StageSplit, "segments", model.TrackRef{}, 0, "need at least two cut points")
	}
	for i := 1; i < len(cuts); i++ {
		if cuts[i].Tick <= cuts[i-1].Tick {
			return nil, model.Errorf(model.StageSplit, "segments", model.TrackRef{}, 0, "cut %d not after cut %d", i, i-1)
		}
	}
	if len(trans) != len(cuts) {
		return nil, model.Errorf(model.StageSplit, "segments", model.TrackRef{}, 0,
			"transition count %d does not match cut count %d", len(trans), len(cuts))
	}
	plan := &Plan{TimelineEnd: tl.End}
	for i := 0; i+1 < len(cuts); i++ {
		span := timeline.Span{Start: cuts[i].Tick, End: cuts[i+1].Tick}
		if !span.Valid() {
			return nil, model.Errorf(model.StageSplit, "segments", model.TrackRef{}, uint64(cuts[i].EventIdx), "segment %d has empty span", i)
		}
		seg := Segment{
			Index:      i,
			Span:       span,
			StartEvent: cuts[i].EventIdx,
			EndEvent:   cuts[i+1].EventIdx,
			StartName:  cuts[i].Source,
			EndName:    cuts[i+1].Source,
		}
		if trans[i].OverlapUs > 0 {
			seg.TransitionIn = trans[i].Name
		}
		if trans[i+1].OverlapUs > 0 {
			seg.TransitionOut = trans[i+1].Name
		}
		// Count samples whose start lies in the half-open span, and the
		// source bytes those samples occupy. Streaming: one pass per track,
		// no buffering of sample payloads.
		for _, tr := range tl.Tracks {
			for _, s := range tr.Samples {
				if s.Start >= span.Start && s.Start < span.End {
					seg.Samples++
					seg.Bytes += uint64(s.Size)
				}
			}
		}
		plan.Segments = append(plan.Segments, seg)
	}
	if len(plan.Segments) == 0 {
		return nil, model.NewError(model.StageSplit, "segments", model.TrackRef{}, 0, "no segments produced")
	}
	plan.Transitions = trans
	if err := checkAdjacency(plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// checkAdjacency enforces the no-gap/no-overlap rule on half-open spans and
// verifies that a cross-fade is referenced from both sides of its boundary.
func checkAdjacency(p *Plan) error {
	for i := 1; i < len(p.Segments); i++ {
		prev, cur := p.Segments[i-1], p.Segments[i]
		if prev.Span.End != cur.Span.Start {
			return model.Errorf(model.StageSplit, "adjacency", model.TrackRef{}, 0,
				"segment %d ends at %d but segment %d starts at %d", prev.Index, prev.Span.End, cur.Index, cur.Span.Start)
		}
	}
	byName := map[string]timeline.Transition{}
	for _, tr := range p.Transitions {
		byName[tr.Name] = tr
	}
	for i := range p.Segments {
		seg := &p.Segments[i]
		if seg.TransitionOut != "" {
			tr, ok := byName[seg.TransitionOut]
			if !ok {
				return model.Errorf(model.StageSplit, "adjacency", model.TrackRef{}, 0, "segment %d references unknown transition %q", i, seg.TransitionOut)
			}
			if i+1 >= len(p.Segments) || p.Segments[i+1].TransitionIn != tr.Name {
				return model.Errorf(model.StageSplit, "adjacency", model.TrackRef{}, uint64(tr.CutTick), "transition %q not mirrored by segment %d", tr.Name, i+1)
			}
			if tr.StartTick < seg.Span.Start || tr.EndTick > p.Segments[i+1].Span.End {
				return model.Errorf(model.StageSplit, "adjacency", model.TrackRef{}, uint64(tr.CutTick), "transition %q window escapes its segments", tr.Name)
			}
		}
	}
	return nil
}

// Describe renders the plan's boundary list for terminal output.
func (p *Plan) Describe() []string {
	out := make([]string, 0, len(p.Segments))
	for _, s := range p.Segments {
		out = append(out, fmt.Sprintf("segment %d [%d,%d) %d samples %d bytes", s.Index, s.Span.Start, s.Span.End, s.Samples, s.Bytes))
	}
	return out
}
