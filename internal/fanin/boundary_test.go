package fanin

import (
	"testing"

	"tsrepair/internal/mediaerr"
	"tsrepair/internal/segment"
)

func seg(rep, id int, start, end int64) *segment.Segment {
	return &segment.Segment{ID: id, Representation: rep, StartNanos: start, EndNanos: end}
}

func TestBoundaryWithinToleranceConsistent(t *testing.T) {
	c := NewChecker(DefaultBoundaryToleranceNanos)
	// 500 microsecond difference, under 1 ms.
	res := c.CheckSegment(0, map[int]*segment.Segment{
		0: seg(0, 0, 1_000_000_000, 2_000_000_000),
		1: seg(1, 0, 1_000_500_000, 2_000_400_000),
	})
	if !res.Consistent || len(res.Mismatches) != 0 {
		t.Fatalf("expected consistent, got %+v", res)
	}
}

func TestBoundaryBeyondToleranceMismatch(t *testing.T) {
	c := NewChecker(DefaultBoundaryToleranceNanos)
	// 2 ms start drift exceeds 1 ms.
	res := c.CheckSegment(0, map[int]*segment.Segment{
		0: seg(0, 0, 1_000_000_000, 2_000_000_000),
		1: seg(1, 0, 1_002_000_000, 2_000_000_000),
	})
	if res.Consistent {
		t.Fatal("expected inconsistent")
	}
	if len(res.Mismatches) != 1 || res.Mismatches[0].Edge != "start" {
		t.Fatalf("mismatches=%+v", res.Mismatches)
	}
	if c.MismatchCount() != 1 {
		t.Fatal("mismatch not recorded with stable code")
	}
	if c.Errors()[0].Code != mediaerr.CodeBoundaryMismatch {
		t.Fatal("wrong error code")
	}
}

func TestThreeRepresentationsTwoMismatches(t *testing.T) {
	c := NewChecker(DefaultBoundaryToleranceNanos)
	res := c.CheckSegment(3, map[int]*segment.Segment{
		0: seg(0, 3, 0, 1_000_000),
		1: seg(1, 3, 0, 1_000_000),
		2: seg(2, 3, 0, 5_000_000), // end 4 ms beyond ref
	})
	if res.Consistent || len(res.Mismatches) != 1 {
		t.Fatalf("got %+v", res)
	}
	if res.Mismatches[0].RepresentationB != 2 {
		t.Error("mismatch should name representation 2")
	}
}

func TestSingleRepresentationConsistent(t *testing.T) {
	c := NewChecker(0) // falls back to default
	res := c.CheckSegment(0, map[int]*segment.Segment{0: seg(0, 0, 10, 20)})
	if !res.Consistent {
		t.Fatal("single representation must be consistent")
	}
}

func TestCheckAllAcrossPositions(t *testing.T) {
	c := NewChecker(DefaultBoundaryToleranceNanos)
	all := map[int][]*segment.Segment{
		0: {seg(0, 0, 0, 100), seg(0, 1, 100, 200)},
		1: {seg(1, 0, 0, 100), seg(1, 1, 100, 5_000_000)},
	}
	results := c.CheckAll(all)
	if len(results) != 2 {
		t.Fatalf("results=%d want 2", len(results))
	}
	if !results[0].Consistent {
		t.Error("position 0 should be consistent")
	}
	if results[1].Consistent {
		t.Error("position 1 should be inconsistent")
	}
	// Deterministic ordering by segment id.
	if results[0].SegmentID != 0 || results[1].SegmentID != 1 {
		t.Error("results not in segment-id order")
	}
}

func TestEmptyInput(t *testing.T) {
	c := NewChecker(DefaultBoundaryToleranceNanos)
	res := c.CheckSegment(0, map[int]*segment.Segment{})
	if res.Consistent {
		t.Error("empty segment set cannot be consistent")
	}
}
