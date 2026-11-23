package ladder

import (
	"testing"

	"mediapack/internal/model"
	"mediapack/internal/timeline"
)

func mtWithPoints(track uint32, ptsNS ...int64) *timeline.MasterTimeline {
	return &timeline.MasterTimeline{
		MasterTrack: track, StartNS: ptsNS[0], EndNS: ptsNS[len(ptsNS)-1],
		CommonPoints: ptsNS,
	}
}

func sec(ns int64) int64 { return ns * 1_000_000_000 }

func TestAlignLadderIntersection(t *testing.T) {
	// Two renditions; shared decodable points are 0,2s,4s (1s/3s only in A).
	a := Representation{ID: "1080p", Bitrate: 5_000_000, Timeline: mtWithPoints(1,
		sec(0), sec(1), sec(2), sec(3), sec(4))}
	b := Representation{ID: "720p", Bitrate: 2_500_000, Timeline: mtWithPoints(2,
		sec(0), sec(2), sec(4))}
	var es model.Errors
	res, err := Align([]Representation{a, b}, Config{
		TargetSegmentNS: sec(2), ToleranceNS: sec(1), MaxRenditions: 8,
	}, &es)
	if err != nil {
		t.Fatalf("alignment should succeed: %v", err)
	}
	want := []int64{sec(0), sec(2), sec(4)}
	if len(res.GlobalBoundaries) != len(want) {
		t.Fatalf("global boundaries = %v, want %v", res.GlobalBoundaries, want)
	}
	for i := range want {
		if res.GlobalBoundaries[i] != want[i] {
			t.Fatalf("boundary %d = %d, want %d", i, res.GlobalBoundaries[i], want[i])
		}
	}
	if len(res.Aligned) != 2 {
		t.Fatalf("aligned renditions = %v", res.Aligned)
	}
	// Deterministic ID ordering.
	if res.Aligned[0] != "1080p" || res.Aligned[1] != "720p" {
		t.Fatalf("renditions must be ID-sorted: %v", res.Aligned)
	}
}

func TestAlignRejectsUnsatisfiableRendition(t *testing.T) {
	// Shared grid forces a 2.4s spacing against a 2s target with tight
	// tolerance: every rendition must be rejected.
	pts := []int64{sec(0), sec(24) / 10, sec(48) / 10, sec(72) / 10}
	a := Representation{ID: "r0", Timeline: mtWithPoints(1, pts...)}
	b := Representation{ID: "r1", Timeline: mtWithPoints(2, pts...)}
	var es model.Errors
	res, err := Align([]Representation{a, b}, Config{
		TargetSegmentNS: sec(2), ToleranceNS: sec(1) / 10,
	}, &es)
	if err == nil {
		t.Fatal("expected rejection error")
	}
	if res == nil || len(res.Rejected) != 2 {
		t.Fatalf("both renditions must be recorded rejected: %+v", res)
	}
	pe, ok := model.AsPipeline(err)
	if !ok || pe.Code != model.CodeAlignTolerance || pe.Stage != model.StageLadder {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestAlignRejectsDisjointGrids(t *testing.T) {
	a := Representation{ID: "a", Timeline: mtWithPoints(1, sec(0), sec(1))}
	b := Representation{ID: "b", Timeline: mtWithPoints(2, sec(5), sec(6))}
	var es model.Errors
	if _, err := Align([]Representation{a, b}, Config{
		TargetSegmentNS: sec(1), ToleranceNS: sec(1),
	}, &es); err == nil {
		t.Fatal("disjoint decodable grids cannot be aligned")
	}
}

func TestAlignRenditionLimit(t *testing.T) {
	var reps []Representation
	for i := 0; i < 5; i++ {
		reps = append(reps, Representation{
			ID:       string(rune('a' + i)),
			Timeline: mtWithPoints(uint32(i+1), sec(0), sec(2)),
		})
	}
	var es model.Errors
	_, err := Align(reps, Config{TargetSegmentNS: sec(2), ToleranceNS: sec(1), MaxRenditions: 3}, &es)
	pe, ok := model.AsPipeline(err)
	if !ok || pe.Code != model.CodeLimitExceeded {
		t.Fatalf("want limit_exceeded, got %v", err)
	}
}
