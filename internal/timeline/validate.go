package timeline

import (
	"fmt"
	"sort"

	"replaycheck/internal/model"
)

// ClipPoint is a validated cut location bound to the event that produced it.
type ClipPoint struct {
	Tick      Tick   `json:"tick"`
	EventIdx  int    `json:"eventIndex"`
	Source    string `json:"source"` // event payload name
	Duplicate bool   `json:"duplicate"`
}

// Transition is a cross-fade centered on one boundary. StartTick/EndTick are
// the half-open overlap window shared by the two adjacent segments.
type Transition struct {
	Index     int    `json:"index"`
	CutTick   Tick   `json:"cutTick"`
	StartTick Tick   `json:"startTick"`
	EndTick   Tick   `json:"endTick"`
	OverlapUs Tick   `json:"overlapUs"`
	Name      string `json:"name"`
}

// Options2 is reserved for validation tunables; kept explicit so callers
// cannot accidentally rely on ambient defaults.
type ValidationOptions struct {
	// MinTransitionUs is the smallest legal cross-fade, inclusive.
	MinTransitionUs Tick
	// MaxTransitionUs caps a cross-fade so it cannot swallow a segment.
	MaxTransitionUs Tick
}

// DefaultValidation returns the acceptance-run validation options.
func DefaultValidation() ValidationOptions {
	return ValidationOptions{MinTransitionUs: 16_667, MaxTransitionUs: 500_000}
}

// ValidateCuts checks that cut points are inside the timeline, strictly
// increasing after duplicate collapsing, and that each is reachable from an
// event. Duplicates are flagged, not dropped: the caller can report them.
func ValidateCuts(tl *Timeline, opts ValidationOptions) ([]ClipPoint, error) {
	if tl == nil {
		return nil, model.NewError(model.StageSplit, "cuts", model.TrackRef{}, 0, "nil timeline")
	}
	if len(tl.Events) == 0 {
		return nil, model.NewError(model.StageSplit, "cuts", model.TrackRef{}, 0, "no trigger events")
	}
	points := make([]ClipPoint, 0, len(tl.Events))
	seen := map[Tick]int{}
	for _, ev := range tl.Events {
		if ev.Tick < tl.Start || ev.Tick > tl.End {
			return nil, model.Errorf(model.StageSplit, "cuts", model.TrackRef{ID: ev.SSRC, Kind: "ssrc"}, uint64(ev.Offset),
				"event %d at tick %d outside timeline [%d,%d]", ev.Index, ev.Tick, tl.Start, tl.End)
		}
		cp := ClipPoint{Tick: ev.Tick, EventIdx: ev.Index, Source: ev.Name}
		if first, ok := seen[ev.Tick]; ok {
			cp.Duplicate = true
			// A repeated trigger at the identical tick is a defect in the
			// event feed, not a second cut. Report it with both event ids.
			return nil, model.Errorf(model.StageSplit, "cuts", model.TrackRef{ID: ev.SSRC, Kind: "ssrc"}, uint64(ev.Offset),
				"duplicate trigger at tick %d: event %d repeats event %d", ev.Tick, ev.Index, first)
		}
		seen[ev.Tick] = ev.Index
		points = append(points, cp)
	}
	sort.SliceStable(points, func(i, j int) bool { return points[i].Tick < points[j].Tick })
	if len(points) < 2 {
		return nil, model.NewError(model.StageSplit, "cuts", model.TrackRef{}, 0, "need at least two cut points")
	}
	for i := 1; i < len(points); i++ {
		if points[i].Tick <= points[i-1].Tick {
			return nil, model.Errorf(model.StageSplit, "cuts", model.TrackRef{}, 0,
				"cut points not strictly increasing at %d", points[i].Tick)
		}
	}
	if points[0].Tick != tl.Start {
		return nil, model.Errorf(model.StageSplit, "cuts", model.TrackRef{}, 0,
			"first cut %d does not open the timeline at %d", points[0].Tick, tl.Start)
	}
	if points[len(points)-1].Tick != tl.End {
		// The final clip would be implicitly closed at the timeline end;
		// require an explicit close event so segment boundaries are traceable.
		return nil, model.Errorf(model.StageSplit, "cuts", model.TrackRef{}, 0,
			"last cut %d does not close the timeline at %d", points[len(points)-1].Tick, tl.End)
	}
	if opts.MinTransitionUs <= 0 || opts.MaxTransitionUs < opts.MinTransitionUs {
		return nil, model.NewError(model.StageSplit, "cuts", model.TrackRef{}, 0, "invalid transition bounds")
	}
	return points, nil
}

// BuildTransitions centers one cross-fade on each interior cut point. The
// overlap is recorded once per boundary; segment metadata references that
// boundary from both sides, which is what makes adjacency verifiable.
func BuildTransitions(points []ClipPoint, opts ValidationOptions) ([]Transition, error) {
	if len(points) < 2 {
		return nil, model.NewError(model.StageSplit, "transition", model.TrackRef{}, 0, "fewer than two cut points")
	}
	out := make([]Transition, 0, len(points))
	for i := 0; i < len(points); i++ {
		// Endpoints have no cross-fade; interior boundaries do.
		if i == 0 || i == len(points)-1 {
			out = append(out, Transition{Index: i, CutTick: points[i].Tick, OverlapUs: 0, Name: "cut"})
			continue
		}
		overlap := opts.MinTransitionUs
		if overlap > opts.MaxTransitionUs {
			return nil, model.Errorf(model.StageSplit, "transition", model.TrackRef{}, 0, "overlap %d exceeds maximum %d", overlap, opts.MaxTransitionUs)
		}
		leftSeg := points[i].Tick - points[i-1].Tick
		rightSeg := points[i+1].Tick - points[i].Tick
		if leftSeg < 2*overlap || rightSeg < 2*overlap {
			return nil, model.Errorf(model.StageSplit, "transition", model.TrackRef{}, 0,
				"boundary %d segments (%d,%d) too short for overlap %d", i, leftSeg, rightSeg, overlap)
		}
		// The window is exactly overlap ticks wide: an odd overlap starts
		// one tick earlier so the cut stays inside [start,end].
		start := points[i].Tick - overlap/2
		if overlap%2 != 0 {
			start--
		}
		out = append(out, Transition{
			Index:     i,
			CutTick:   points[i].Tick,
			StartTick: start,
			EndTick:   start + overlap,
			OverlapUs: overlap,
			Name:      fmt.Sprintf("xfade-%d", i),
		})
	}
	return out, nil
}
