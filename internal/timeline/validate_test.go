package timeline

import (
	"testing"

	"replaycheck/internal/model"
)

func points(ticks ...Tick) []ClipPoint {
	out := make([]ClipPoint, len(ticks))
	for i, t := range ticks {
		out[i] = ClipPoint{Tick: t, EventIdx: i}
	}
	return out
}

func TestBuildTransitionsCenteredAndConsistent(t *testing.T) {
	cps := points(0, 2_000_000, 4_000_000, 6_000_000)
	trs, err := BuildTransitions(cps, DefaultValidation())
	if err != nil {
		t.Fatal(err)
	}
	if len(trs) != len(cps) {
		t.Fatalf("want one transition per cut, got %d", len(trs))
	}
	for _, tr := range trs {
		if tr.Index == 0 || tr.Index == len(trs)-1 {
			if tr.OverlapUs != 0 {
				t.Fatalf("endpoint %d must not cross-fade", tr.Index)
			}
			continue
		}
		if tr.EndTick-tr.StartTick != tr.OverlapUs {
			t.Fatalf("transition %d window %d != overlap %d", tr.Index, tr.EndTick-tr.StartTick, tr.OverlapUs)
		}
		if tr.StartTick > tr.CutTick || tr.EndTick < tr.CutTick {
			t.Fatalf("transition %d does not straddle cut %d", tr.Index, tr.CutTick)
		}
	}
}

func TestBuildTransitionsRejectsOversizedOverlap(t *testing.T) {
	cps := points(0, 20_000, 40_000)
	opts := ValidationOptions{MinTransitionUs: 25_000, MaxTransitionUs: 500_000}
	if _, err := BuildTransitions(cps, opts); err == nil {
		t.Fatal("expected overlap to exceed adjacent segment")
	}
}

func TestValidateCutsDuplicate(t *testing.T) {
	tl := &Timeline{Start: 0, End: 100,
		Events: []Event{
			{Tick: 0, Index: 0},
			{Tick: 50, Index: 1, Offset: 10},
			{Tick: 50, Index: 2, Offset: 20},
			{Tick: 100, Index: 3},
		}}
	_, err := ValidateCuts(tl, DefaultValidation())
	if err == nil {
		t.Fatal("expected duplicate error")
	}
	me, ok := model.AsError(err)
	if !ok || me.Stage != model.StageSplit || me.Offset != 20 {
		t.Fatalf("bad error %v", err)
	}
}

func TestValidateCutsUnclosedTimeline(t *testing.T) {
	tl := &Timeline{Start: 0, End: 100, Events: []Event{{Tick: 0}, {Tick: 40}}}
	if _, err := ValidateCuts(tl, DefaultValidation()); err == nil {
		t.Fatal("expected unclosed timeline error")
	}
}
