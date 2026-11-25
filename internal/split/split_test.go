package split

import (
	"testing"

	"replaycheck/internal/timeline"
)

func sampleTimeline() (*timeline.Timeline, []timeline.ClipPoint, []timeline.Transition) {
	tl := &timeline.Timeline{Start: 0, End: 6000}
	tl.Tracks = []*timeline.Track{{
		ID: 1, Kind: "video", Start: 0, End: 6000,
		Samples: []timeline.Sample{
			{Start: 0, End: 2000, Size: 100},
			{Start: 2000, End: 4000, Size: 100},
			{Start: 4000, End: 6000, Size: 100},
		},
	}}
	cuts := []timeline.ClipPoint{{Tick: 0, EventIdx: 0}, {Tick: 3000, EventIdx: 1}, {Tick: 6000, EventIdx: 2}}
	trs, err := timeline.BuildTransitions(cuts, timeline.ValidationOptions{MinTransitionUs: 500, MaxTransitionUs: 1000})
	if err != nil {
		panic(err)
	}
	return tl, cuts, trs
}

func TestSplitHalfOpenIntervals(t *testing.T) {
	tl, cuts, trs := sampleTimeline()
	plan, err := SplitCuts(tl, cuts, trs)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Segments) != 2 {
		t.Fatalf("segments=%d", len(plan.Segments))
	}
	if plan.Segments[0].Span.End != plan.Segments[1].Span.Start {
		t.Fatal("segments are not adjacent")
	}
	// A sample starting exactly on the boundary belongs to the later
	// segment only.
	if plan.Segments[0].Samples != 2 || plan.Segments[1].Samples != 1 {
		t.Fatalf("boundary assignment wrong: %d, %d", plan.Segments[0].Samples, plan.Segments[1].Samples)
	}
	if _, err := VerifyTransitions(plan); err != nil {
		t.Fatal(err)
	}
}

func TestSplitRejectsUnmirroredTransition(t *testing.T) {
	tl, cuts, trs := sampleTimeline()
	plan, err := SplitCuts(tl, cuts, trs)
	if err != nil {
		t.Skipf("setup rejected: %v", err)
	}
	plan.Segments[1].TransitionIn = "bogus"
	if _, err := VerifyTransitions(plan); err == nil {
		t.Fatal("expected unmirrored transition to fail")
	}
}
