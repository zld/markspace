package pipeline

import "testing"

func TestAcceptanceMetrics(t *testing.T) {
	res, err := Run(Config{LateWindowMicros: 3, ShardWindowMicros: 100})
	if err != nil {
		t.Fatalf("pipeline run: %v", err)
	}
	if res.ParseRoundTrip != 1.0 {
		t.Errorf("parse_roundtrip = %f want 1.0", res.ParseRoundTrip)
	}
	if res.KeyframeHit != 1.0 {
		t.Errorf("keyframe_hit = %f want 1.0", res.KeyframeHit)
	}
	if res.BadSegments < 2 {
		t.Errorf("bad_segments = %d want >=2", res.BadSegments)
	}
	if res.OffsetTrace != 1.0 {
		t.Errorf("offset_trace = %f want 1.0", res.OffsetTrace)
	}
	if !res.ReplayEqual {
		t.Errorf("replay_equal = false want true")
	}
	if res.SegmentSHA256Count < 1 {
		t.Errorf("segment_sha256_count = %d want >=1", res.SegmentSHA256Count)
	}
	if res.SummaryHashForward == "" || res.SummaryHashForward != res.SummaryHashReverse {
		t.Errorf("forward/reverse summary hashes differ: %q vs %q",
			res.SummaryHashForward, res.SummaryHashReverse)
	}
	if len(res.Notes) != 0 {
		t.Errorf("pipeline notes: %v", res.Notes)
	}
}

func TestReplayDeterminismIndependentOfOrder(t *testing.T) {
	res, err := Run(Config{LateWindowMicros: 3, ShardWindowMicros: 100})
	if err != nil {
		t.Fatal(err)
	}
	if res.SummaryHashForward != res.SummaryHashReverse {
		t.Fatal("late-frame reorder changed the summary hash")
	}
	if res.RecomputeForward == 0 || res.RecomputeReverse == 0 {
		t.Fatal("late frames must have triggered deterministic recompute")
	}
}

func TestBadSegmentsAreSeparatedByValidFrames(t *testing.T) {
	res, err := Run(Config{LateWindowMicros: 3, ShardWindowMicros: 100})
	if err != nil {
		t.Fatal(err)
	}
	// The CRC fixture has two isolated bad frames, each its own segment.
	if res.BadSegments < 2 {
		t.Fatalf("expected isolated damage segments, got %d", res.BadSegments)
	}
}
