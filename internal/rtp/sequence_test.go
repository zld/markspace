package rtp

import "testing"

func TestWraparoundNoFalseGap(t *testing.T) {
	// Independent small sample from the acceptance description:
	// 65534, 65535, 0, 1 is one contiguous run across the wraparound.
	c := NewSequenceChecker()
	for _, seq := range []uint16{65534, 65535, 0, 1} {
		if g := c.Observe(1, seq); g != nil {
			t.Fatalf("unexpected gap at seq %d: %v", seq, g)
		}
	}
	c.Finalize()
	if got := c.GapCount(); got != 0 {
		t.Fatalf("gap count = %d, want 0", got)
	}
}

func TestSingleGapRecordedOnFinalize(t *testing.T) {
	// 10,11,13,14: seq 12 never arrives. The gap is charged when the
	// stream is finalized because 13 stayed buffered past its hole.
	c := NewSequenceChecker()
	for _, seq := range []uint16{10, 11, 13, 14} {
		c.Observe(7, seq)
	}
	if c.GapCount() != 0 {
		t.Fatalf("gap charged before finalize: %v", c.Gaps())
	}
	c.Finalize()
	gaps := c.Gaps()
	if len(gaps) != 1 || c.GapCount() != 1 {
		t.Fatalf("gaps=%v count=%d, want one gap of one", gaps, c.GapCount())
	}
	g := gaps[0]
	if g.FirstMissing != 12 || g.MissingCount != 1 ||
		g.AfterPrev != 11 || g.BeforeNext != 13 || g.SSRC != 7 {
		t.Fatalf("gap details wrong: %+v", g)
	}
}

func TestLargeJumpChargedImmediately(t *testing.T) {
	// A jump beyond the reorder window cannot be a late reorder: the
	// whole skipped range is reported without waiting for Finalize.
	c := NewSequenceChecker()
	c.Observe(1, 10)
	g := c.Observe(1, 10+reorderWindow+5)
	if g == nil {
		t.Fatal("expected immediate gap for large jump")
	}
	if g.MissingCount != reorderWindow+4 {
		t.Fatalf("missing = %d, want %d", g.MissingCount, reorderWindow+4)
	}
}

func TestWraparoundGap(t *testing.T) {
	// 65534, 65535, 1 (seq 0 lost): missing 0 across the wrap point.
	c := NewSequenceChecker()
	c.Observe(1, 65534)
	c.Observe(1, 65535)
	if g := c.Observe(1, 1); g != nil {
		t.Fatalf("gap should stay pending until finalize, got %v", g)
	}
	c.Finalize()
	gaps := c.Gaps()
	if len(gaps) != 1 {
		t.Fatalf("gaps = %v, want one", gaps)
	}
	g := gaps[0]
	if g.FirstMissing != 0 || g.MissingCount != 1 ||
		g.BeforeNext != 1 || g.AfterPrev != 65535 {
		t.Fatalf("gap across wrap wrong: %+v", g)
	}
	if c.GapCount() != 1 {
		t.Fatalf("gap count = %d, want 1", c.GapCount())
	}
}

func TestReorderNoFalseGap(t *testing.T) {
	// Out-of-order arrival of a complete run: 11, 10, 12. Seq 12 stays
	// buffered until 10/11 establish the head, then fills cleanly.
	c := NewSequenceChecker()
	for _, seq := range []uint16{11, 10, 12} {
		if g := c.Observe(3, seq); g != nil {
			t.Fatalf("unexpected gap for reordered seq %d: %v", seq, g)
		}
	}
	c.Finalize()
	if c.GapCount() != 0 {
		t.Fatalf("gap count = %d, want 0", c.GapCount())
	}
}

func TestPerSSRCIndependence(t *testing.T) {
	c := NewSequenceChecker()
	c.Observe(100, 5) // baseline SSRC 100
	c.Observe(200, 9) // baseline SSRC 200
	c.Observe(100, 7) // gap of 6 on SSRC 100 only
	c.Observe(200, 10)
	c.Finalize()
	if c.GapCount() != 1 || c.Gaps()[0].SSRC != 100 {
		t.Fatalf("gaps = %v, want single gap on SSRC 100", c.Gaps())
	}
}

func TestGapDoesNotResetAfterBadFrame(t *testing.T) {
	// Recovery case: after a gap the cumulative head keeps advancing,
	// so later continuity checks are not reset by the bad frame.
	c := NewSequenceChecker()
	for _, seq := range []uint16{20, 22, 23, 24, 25} {
		c.Observe(1, seq)
	}
	c.Finalize()
	if c.GapCount() != 1 {
		t.Fatalf("gap count = %d, want 1", c.GapCount())
	}
	g := c.Gaps()[0]
	if g.FirstMissing != 21 || g.BeforeNext != 22 {
		t.Fatalf("gap details wrong: %+v", g)
	}
}

func TestLateFillClearsPendingHole(t *testing.T) {
	// 40, 42, 41: 42 arrives first and buffers; 41 then fills the only
	// hole, so Finalize reports nothing.
	c := NewSequenceChecker()
	for _, seq := range []uint16{40, 42, 41} {
		c.Observe(9, seq)
	}
	c.Finalize()
	if c.GapCount() != 0 {
		t.Fatalf("late fill produced gap: %v", c.Gaps())
	}
}

func TestEndStreamChargesWithheldTail(t *testing.T) {
	// Only seq 5 is observed, but the scenario declares seq 6 as the
	// stream tail. The transport withheld nothing else, yet the tail
	// loss must still be charged deterministically.
	c := NewSequenceChecker()
	c.Observe(1, 5)
	if g := c.EndStream(1, 6); g == nil || g.MissingCount != 1 ||
		g.FirstMissing != 6 || g.AfterPrev != 5 {
		t.Fatalf("tail gap wrong: %+v", g)
	}
	if c.GapCount() != 1 {
		t.Fatalf("gap count = %d, want 1", c.GapCount())
	}
}

func TestEndStreamNoopWhenTailObserved(t *testing.T) {
	c := NewSequenceChecker()
	for _, seq := range []uint16{10, 11, 12} {
		c.Observe(1, seq)
	}
	if g := c.EndStream(1, 12); g != nil {
		t.Fatalf("unexpected gap when tail was observed: %v", g)
	}
	if c.GapCount() != 0 {
		t.Fatalf("gap count = %d, want 0", c.GapCount())
	}
}

func TestEndStreamCountsMultipleMissingTail(t *testing.T) {
	c := NewSequenceChecker()
	c.Observe(1, 20)
	g := c.EndStream(1, 23)
	if g == nil || g.MissingCount != 3 {
		t.Fatalf("got %+v, want 3 missing (21,22,23)", g)
	}
}
