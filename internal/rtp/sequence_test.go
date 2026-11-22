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
	if got := c.GapCount(); got != 0 {
		t.Fatalf("gap count = %d, want 0", got)
	}
}

func TestSingleGapRecorded(t *testing.T) {
	c := NewSequenceChecker()
	for _, seq := range []uint16{10, 11, 13, 14} {
		c.Observe(7, seq)
	}
	gaps := c.Gaps()
	if len(gaps) != 1 || c.GapCount() != 1 {
		t.Fatalf("got gaps=%v count=%d, want one gap of one", gaps, c.GapCount())
	}
	g := gaps[0]
	if g.FirstMissing != 12 || g.MissingCount != 1 ||
		g.AfterPrev != 11 || g.BeforeNext != 13 || g.SSRC != 7 {
		t.Fatalf("gap details wrong: %+v", g)
	}
}

func TestWraparoundWithGap(t *testing.T) {
	// 65534, 65535, 1 (seq 0 lost): missing 0 across the wrap point.
	c := NewSequenceChecker()
	c.Observe(1, 65534)
	c.Observe(1, 65535)
	g := c.Observe(1, 1)
	if g == nil {
		t.Fatal("expected gap across wraparound")
	}
	if g.FirstMissing != 0 || g.MissingCount != 1 {
		t.Fatalf("got %+v, want firstMissing=0 count=1", g)
	}
	if c.GapCount() != 1 {
		t.Fatalf("gap count = %d, want 1", c.GapCount())
	}
}

func TestReorderNoFalseGap(t *testing.T) {
	// Out-of-order arrival of a complete run: 11, 10, 12.
	c := NewSequenceChecker()
	for _, seq := range []uint16{11, 10, 12} {
		if g := c.Observe(3, seq); g != nil {
			t.Fatalf("unexpected gap for reordered seq %d: %v", seq, g)
		}
	}
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
	if c.GapCount() != 1 || c.Gaps()[0].SSRC != 100 {
		t.Fatalf("gaps = %v, want single gap on SSRC 100", c.Gaps())
	}
}

func TestGapDoesNotResetAfterBadFrame(t *testing.T) {
	// Recovery case: after a gap the cumulative head keeps advancing,
	// so later continuity checks are not reset by the bad frame.
	c := NewSequenceChecker()
	for _, seq := range []uint16{20, 22, 23, 24} {
		c.Observe(1, seq)
	}
	if c.GapCount() != 1 {
		t.Fatalf("gap count = %d, want 1", c.GapCount())
	}
	// A further contiguous packet must not manufacture another gap.
	if g := c.Observe(1, 25); g != nil {
		t.Fatalf("unexpected second gap: %v", g)
	}
	if c.GapCount() != 1 {
		t.Fatalf("gap count = %d, still want 1", c.GapCount())
	}
}
