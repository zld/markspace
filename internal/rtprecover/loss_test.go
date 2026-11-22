package rtprecover

import (
	"testing"

	"github.com/archive/mediakernel/internal/manifest"
	"github.com/archive/mediakernel/internal/timeline"
)

func observeSeq(t *testing.T, d *Detector, seq uint16, us int64) {
	t.Helper()
	d.Observe(OrderedPacket{Packet: pkt(seq), TimelineUS: us})
}

func TestBurstBelowThresholdNotReported(t *testing.T) {
	d, err := NewDetector(5)
	if err != nil {
		t.Fatal(err)
	}
	// 0, then 2 (one missing): sub-threshold, must not become a gap.
	observeSeq(t, d, 0, 0)
	observeSeq(t, d, 2, 40_000)
	if gaps := d.Finish(40_000); len(gaps) != 0 {
		t.Fatalf("sub-threshold loss reported as gap: %+v", gaps)
	}
	if d.TotalMissing() != 1 {
		t.Fatalf("total missing = %d, want 1", d.TotalMissing())
	}
}

func TestBurstAtThresholdRecordedWithBounds(t *testing.T) {
	d, _ := NewDetector(3)
	observeSeq(t, d, 10, 1_000_000)
	// Sequences 11..17 missing (7 packets), 18 arrives.
	observeSeq(t, d, 18, 9_000_000)
	gaps := d.Finish(9_000_000)
	if len(gaps) != 1 {
		t.Fatalf("gaps = %+v, want exactly one burst", gaps)
	}
	g := gaps[0]
	if g.MissingCount != 7 {
		t.Fatalf("missing count = %d, want 7", g.MissingCount)
	}
	if g.StartUS != 1_000_000 || g.EndUS != 9_000_000 {
		t.Fatalf("gap bounds = [%d,%d), want [1e6,9e6)", g.StartUS, g.EndUS)
	}
}

func TestSeparateBurstsStaySeparate(t *testing.T) {
	d, _ := NewDetector(3)
	observeSeq(t, d, 0, 0)
	observeSeq(t, d, 5, 5_000)   // 1..4 missing -> burst
	observeSeq(t, d, 6, 6_000)   // contiguous
	observeSeq(t, d, 12, 12_000) // 7..11 missing -> second burst
	gaps := d.Finish(12_000)
	if len(gaps) != 2 {
		t.Fatalf("gaps = %+v, want 2 distinct bursts", gaps)
	}
	if gaps[0].MissingCount != 4 || gaps[1].MissingCount != 5 {
		t.Fatalf("counts = %d,%d want 4,5", gaps[0].MissingCount, gaps[1].MissingCount)
	}
}

func TestBurstAcrossWraparound(t *testing.T) {
	d, _ := NewDetector(3)
	observeSeq(t, d, 65533, 0)
	observeSeq(t, d, 65534, 1_000)
	// 65535, 0, 1, 2 missing; 3 arrives. Modular distance must count 4, not 65536.
	observeSeq(t, d, 3, 7_000)
	gaps := d.Finish(7_000)
	if len(gaps) != 1 || gaps[0].MissingCount != 4 {
		t.Fatalf("wrap gap = %+v, want one gap of 4", gaps)
	}
}

func TestReorderedThenFilledProducesNoGap(t *testing.T) {
	// End to end through the window: an apparent hole that fills within the
	// window must never be confirmed as loss.
	w, _ := NewWindow(8)
	d, _ := NewDetector(1)
	for _, s := range []uint16{0, 3, 1, 2, 4} {
		out, err := w.Push(pkt(s), int64(s)*1_000)
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range out {
			d.Observe(op)
		}
	}
	for _, op := range w.Flush() {
		d.Observe(op)
	}
	if gaps := d.Finish(5_000); len(gaps) != 0 {
		t.Fatalf("filled reordering reported as loss: %+v", gaps)
	}
}

func TestCoversIntervalOverlap(t *testing.T) {
	gaps := []manifest.LossGap{{StartUS: 10_000, EndUS: 20_000, MissingCount: 3}}
	if !Covers(gaps, 15_000, 16_000) {
		t.Fatal("interval fully inside gap must be covered")
	}
	if !Covers(gaps, 19_000, 25_000) {
		t.Fatal("interval crossing gap end must be covered")
	}
	if Covers(gaps, 20_000, 21_000) {
		t.Fatal("interval starting exactly at gap end is not covered")
	}
	if Covers(gaps, 0, 10_000) {
		t.Fatal("interval ending exactly at gap start is not covered")
	}
}

func TestDetectorRejectsBadThreshold(t *testing.T) {
	if _, err := NewDetector(0); timeline.Classify(err) != timeline.KindInvalidInput {
		t.Fatalf("zero threshold: %v", err)
	}
}
