package timeline

import (
	"testing"

	"regress/internal/clock"
	"regress/internal/model"
)

func evt(key, track string, seq uint32, tick clock.Tick) model.Event {
	return model.Event{Key: key, Track: track, Seq: seq,
		ObservedTick: tick, Delay: tick}
}

func planItem(key, track string, seq uint32, tick clock.Tick) model.PlanItem {
	return model.PlanItem{Key: key, Track: track, Seq: seq, ExpectedTick: tick}
}

func TestDedupByStableKey(t *testing.T) {
	mp4 := []model.Track{{
		ID: "t1", Kind: model.KindID3,
		Events: []model.Event{
			evt("k1", "t1", 0, 100),
			evt("k1", "t1", 0, 100), // same key repeated
			evt("k2", "t1", 1, 200),
		},
	}}
	tl := Build(mp4, nil, []model.PlanItem{
		planItem("k1", "t1", 0, 90),
		planItem("k2", "t1", 1, 190),
	})
	if len(tl.Occurrences) != 3 {
		t.Fatalf("occurrences = %d, want 3", len(tl.Occurrences))
	}
	if len(tl.Unique) != 2 {
		t.Fatalf("unique = %d, want 2", len(tl.Unique))
	}
	// k1 must be flagged duplicate via alignment, k2 clean.
	var dup, missing int
	for _, a := range tl.Alignment {
		if a.Duplicate {
			dup++
		}
		if a.Missing {
			missing++
		}
	}
	if dup != 1 || missing != 0 {
		t.Fatalf("dup=%d missing=%d, want 1/0", dup, missing)
	}
}

func TestSameTickConflictStableOrder(t *testing.T) {
	mp4 := []model.Track{
		{ID: "b", Kind: model.KindSEI, Events: []model.Event{
			evt("b-1", "b", 5, 100), evt("b-0", "b", 2, 100)}},
		{ID: "a", Kind: model.KindID3, Events: []model.Event{
			evt("a-0", "a", 9, 100)}},
	}
	tl := Build(mp4, nil, nil)
	got := []string{
		tl.Occurrences[0].Event.Track,
		tl.Occurrences[1].Event.Track,
		tl.Occurrences[2].Event.Track,
	}
	want := []string{"a", "b", "b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	// Within track b, seq 2 must precede seq 5.
	if tl.Occurrences[1].Event.Seq != 2 || tl.Occurrences[2].Event.Seq != 5 {
		t.Fatalf("same-track tie not seq-ordered: %d %d",
			tl.Occurrences[1].Event.Seq, tl.Occurrences[2].Event.Seq)
	}
}

func TestMissingAlignment(t *testing.T) {
	tl := Build(nil, nil, []model.PlanItem{planItem("ghost", "x", 0, 50)})
	if len(tl.Alignment) != 1 || !tl.Alignment[0].Missing {
		t.Fatalf("expected one missing alignment, got %+v", tl.Alignment)
	}
	if _, ok := tl.Lookup("ghost"); ok {
		t.Fatal("missing key must not resolve")
	}
}

func TestMP4AndRTPSourcesTotalOrder(t *testing.T) {
	mp4 := []model.Track{{ID: "m", Kind: model.KindEmsg,
		Events: []model.Event{evt("k", "m", 1, 100)}}}
	rtp := []SourceStream{{TrackID: "m", Kind: model.KindEmsg,
		Events: []model.Event{evt("k", "m", 1, 100)}}}
	tl := Build(mp4, rtp, []model.PlanItem{planItem("k", "m", 1, 100)})
	if len(tl.Occurrences) != 2 {
		t.Fatalf("occurrences = %d, want 2 (mp4 + rtp)", len(tl.Occurrences))
	}
	if tl.Occurrences[0].Source != "mp4:m" || tl.Occurrences[1].Source != "rtp:m" {
		t.Fatalf("source tie order wrong: %q %q",
			tl.Occurrences[0].Source, tl.Occurrences[1].Source)
	}
	// One sighting per source is cross-source confirmation, not a duplicate.
	if tl.Alignment[0].Duplicate {
		t.Fatal("cross-source sighting must not count as a duplicate")
	}
}

func TestWithinSourceRepeatIsDuplicate(t *testing.T) {
	mp4 := []model.Track{{ID: "m", Kind: model.KindEmsg,
		Events: []model.Event{evt("k", "m", 1, 100), evt("k", "m", 1, 100)}}}
	rtp := []SourceStream{{TrackID: "m", Kind: model.KindEmsg,
		Events: []model.Event{evt("k", "m", 1, 100)}}}
	tl := Build(mp4, rtp, []model.PlanItem{planItem("k", "m", 1, 100)})
	if !tl.Alignment[0].Duplicate {
		t.Fatal("two occurrences within the MP4 source must count as duplicate")
	}
}

func TestAlignmentDelayPopulated(t *testing.T) {
	mp4 := []model.Track{{ID: "m", Kind: model.KindID3,
		Events: []model.Event{evt("k", "m", 1, 1150)}}}
	tl := Build(mp4, nil, []model.PlanItem{planItem("k", "m", 1, 1000)})
	a := tl.Alignment[0]
	if a.Missing || a.Delay != 150 {
		t.Fatalf("delay = %d, missing=%v, want 150", a.Delay, a.Missing)
	}
	// A missing item carries no delay rather than a stale/negative value.
	tl2 := Build(nil, nil, []model.PlanItem{planItem("gone", "m", 2, 1000)})
	if tl2.Alignment[0].Delay != 0 {
		t.Fatalf("missing item delay = %d, want 0", tl2.Alignment[0].Delay)
	}
}

func TestCrossSourceTickDisagreementIsConflict(t *testing.T) {
	mp4 := []model.Track{{ID: "m", Kind: model.KindEmsg,
		Events: []model.Event{evt("k", "m", 1, 100)}}}
	rtp := []SourceStream{{TrackID: "m", Kind: model.KindEmsg,
		Events: []model.Event{evt("k", "m", 1, 103)}}}
	tl := Build(mp4, rtp, []model.PlanItem{planItem("k", "m", 1, 100)})
	a := tl.Alignment[0]
	if !a.Conflict {
		t.Fatal("sources reporting different ticks must flag a conflict")
	}
	if a.Duplicate {
		t.Fatal("cross-source disagreement is a conflict, not a duplicate")
	}
	// The earliest ordered occurrence defines the reported tick: mp4 at 100
	// precedes rtp at 103.
	if a.ObservedTick != 100 {
		t.Fatalf("observed tick = %d, want 100", a.ObservedTick)
	}
}
