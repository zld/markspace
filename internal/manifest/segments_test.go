package manifest

import (
	"testing"

	"mediapack/internal/model"
	"mediapack/internal/timeline"
)

// master builds a MasterTimeline whose common points are given in milliseconds.
func master(pointsMS ...int64) *timeline.MasterTimeline {
	points := make([]int64, len(pointsMS))
	for i, ms := range pointsMS {
		points[i] = ms * 1_000_000
	}
	return &timeline.MasterTimeline{
		MasterTrack:  1,
		StartNS:      points[0],
		EndNS:        points[len(points)-1],
		CommonPoints: points,
		Tracks:       []*timeline.TrackTimeline{{TrackID: 1}},
	}
}

func TestBuildExactSegments(t *testing.T) {
	// Points every second over 6 s; target 2 s -> three exact segments.
	mt := master(0, 1000, 2000, 3000, 4000, 5000, 6000)
	var es model.Errors
	plan, err := Build(mt, Config{TargetSegmentNS: 2_000_000_000, ToleranceNS: 250_000_000}, &es)
	if err != nil {
		t.Fatalf("exact segmentation must pass: %v", err)
	}
	if len(plan.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(plan.Segments))
	}
	for _, s := range plan.Segments {
		if s.DeviationNS != 0 || s.ActualNS != 2_000_000_000 {
			t.Fatalf("segment not exact: %+v", s)
		}
	}
}

func TestBuildRecordsDeviation(t *testing.T) {
	// VFR-ish points: closest boundaries to 2s are at 0, 1900ms, 4100ms.
	mt := master(0, 950, 1900, 3000, 4100, 5000)
	var es model.Errors
	plan, err := Build(mt, Config{TargetSegmentNS: 2_000_000_000, ToleranceNS: 200_000_000}, &es)
	if err != nil {
		t.Fatalf("within-tolerance deviation must pass: %v", err)
	}
	var totalDev int64
	for _, s := range plan.Segments {
		totalDev += s.DeviationNS
		if !s.Partial && s.DeviationNS > 200_000_000 {
			t.Fatalf("non-tail deviation beyond tolerance: %+v", s)
		}
	}
	if totalDev == 0 {
		t.Fatal("VFR boundaries must record non-zero actual deviation")
	}
	last := plan.Segments[len(plan.Segments)-1]
	if !last.Partial || last.DeviationNS == 0 {
		t.Fatalf("shorter tail segment must be Partial with deviation: %+v", last)
	}
}

func TestBuildRejectsBeyondTolerance(t *testing.T) {
	// Points 1.2s apart force a 2.4s first segment against a 2s target with a
	// tight 100ms tolerance -> must reject and surface structured error.
	mt := master(0, 1200, 2400, 3600, 4800)
	var es model.Errors
	plan, err := Build(mt, Config{TargetSegmentNS: 2_000_000_000, ToleranceNS: 100_000_000}, &es)
	if err == nil || !plan.Rejected {
		t.Fatalf("beyond-tolerance plan must be rejected, plan=%+v err=%v", plan, err)
	}
	pe, ok := model.AsPipeline(err)
	if !ok || pe.Code != model.CodeAlignTolerance || pe.Stage != model.StageManifest {
		t.Fatalf("wrong rejection error: %v", err)
	}
}

func TestBuildRejectsTooFewPoints(t *testing.T) {
	mt := master(1000)
	var es model.Errors
	if _, err := Build(mt, Config{TargetSegmentNS: 1_000_000_000, ToleranceNS: 100_000_000}, &es); err == nil {
		t.Fatal("single common point cannot form a segment")
	}
}

func TestContentHashDeterministic(t *testing.T) {
	mt := master(0, 1000, 2000, 3000, 4000)
	cfg := Config{TargetSegmentNS: 2_000_000_000, ToleranceNS: 500_000_000}
	var es1, es2 model.Errors
	p1, err1 := Build(mt, cfg, &es1)
	p2, err2 := Build(mt, cfg, &es2)
	if err1 != nil || err2 != nil {
		t.Fatal(err1)
	}
	if p1.ContentHash() != p2.ContentHash() || len(p1.ContentHash()) != 64 {
		t.Fatal("repeated builds must yield identical 256-bit hashes")
	}
}

func TestBoundaryCoversCommonPointsOnly(t *testing.T) {
	// Every segment start/end must be one of the declared common points.
	mt := master(0, 1000, 2000, 3000, 4000, 5000)
	var es model.Errors
	plan, err := Build(mt, Config{TargetSegmentNS: 1_500_000_000, ToleranceNS: 1_000_000_000}, &es)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[int64]bool{}
	for _, p := range mt.CommonPoints {
		allowed[p] = true
	}
	for _, s := range plan.Segments {
		if !allowed[s.StartNS] || !allowed[s.EndNS] {
			t.Fatalf("segment boundary not a common decodable point: %+v", s)
		}
	}
}
