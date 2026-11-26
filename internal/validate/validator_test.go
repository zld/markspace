package validate

import (
	"strings"
	"testing"

	"pwflow/internal/pipeline"
)

func TestCheckCountsAndAlignmentThreshold(t *testing.T) {
	m := &pipeline.Manifest{
		Status: pipeline.StatusPass,
		Counts: pipeline.Counts{Segments: 1, SilenceIntervals: 0},
		Segments: []pipeline.SegmentResult{{
			Frames: []pipeline.FrameInfo{{SHA256: "a"}, {SHA256: "b"}},
		}},
		Alignment: pipeline.Alignment{MaxErrorNS: 10_000_000, MaxErrorMS: 10},
	}
	m.Counts.Segments = len(m.Segments)
	rep := &Report{Manifest: m}
	checkCountsAndStatus(rep, m)
	if len(rep.Failures) != 0 {
		t.Fatalf("unexpected failures: %v", rep.Failures)
	}
	if root := hashFrameRoot(m.Segments[0].Frames); root == "" || len(root) != 64 {
		t.Fatalf("bad frame root %q", root)
	}

	m.Alignment.MaxErrorNS = 41_000_000
	m.Alignment.MaxErrorMS = 41
	rep = &Report{Manifest: m}
	checkCountsAndStatus(rep, m)
	if len(rep.Failures) == 0 || !strings.Contains(rep.Failures[0], "alignment-threshold") {
		t.Fatalf("expected alignment failure, got %+v", rep.Failures)
	}
}

func TestNearestDistance(t *testing.T) {
	keys := []int64{0, 1_000_000_000, 2_000_000_000}
	if d := nearestDistance(1_040_000_000, keys); d != 40_000_000 {
		t.Fatalf("distance=%d", d)
	}
	if d := nearestDistance(500, nil); d == 0 {
		t.Fatal("expected maximum distance for no keyframes")
	}
}
