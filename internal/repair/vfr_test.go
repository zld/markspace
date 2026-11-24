package repair

import (
	"testing"

	"mediasync/internal/clock"
)

func samplesAt(ns ...int64) []Sample {
	out := make([]Sample, len(ns))
	for i, v := range ns {
		out[i] = Sample{Index: i, MediaNS: v}
	}
	return out
}

func TestFrameDurationsMonotonic(t *testing.T) {
	s := samplesAt(0, 100, 200, 300)
	d, err := FrameDurations(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(d) != 4 || d[0] != 100 || d[2] != 100 || d[3] != 100 {
		t.Fatalf("durations wrong: %v", d)
	}
}

func TestFrameDurationsRepairsZeroDelta(t *testing.T) {
	s := samplesAt(0, 100, 100, 200) // duplicated timestamp in the middle
	d, err := FrameDurations(s)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range d {
		if v <= 0 {
			t.Fatalf("non-positive duration at %d: %d", i, v)
		}
	}
}

func TestAnalyzeVFRDetectsIrregularEdges(t *testing.T) {
	// 100ms cadence with one doubled interval (a dropped frame / VFR change).
	s := samplesAt(0, 100, 200, 400, 500, 600)
	stats := AnalyzeVFR(s)
	if stats.NominalNS != 100 {
		t.Fatalf("nominal=%d want 100", stats.NominalNS)
	}
	if stats.IrregularEdges < 1 {
		t.Fatalf("irregular edge not detected: %+v", stats)
	}
	if stats.MinDurationNS != 100 || stats.MaxDurationNS != 200 {
		t.Fatalf("min/max wrong: %+v", stats)
	}
}

func TestSegmentGridBoundAndContiguity(t *testing.T) {
	// 30 frames at 100 ms, targeting ~250 ms segments.
	ns := make([]int64, 30)
	for i := range ns {
		ns[i] = int64(i) * 100
	}
	samples := samplesAt(ns...)
	d, err := FrameDurations(samples)
	if err != nil {
		t.Fatal(err)
	}
	segs, err := SegmentGrid(samples, d, 250)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) < 8 {
		t.Fatalf("segment count=%d too low", len(segs))
	}
	for i := 1; i < len(segs); i++ {
		if segs[i].StartSample != segs[i-1].EndSample {
			t.Fatalf("segments not contiguous at %d", i)
		}
		if segs[i].StartNS < segs[i-1].StartNS {
			t.Fatal("segment start regressed")
		}
	}
	if segs[len(segs)-1].EndSample != 30 {
		t.Fatal("final segment does not cover all samples")
	}
}

func TestSegmentGridRejectsBadTarget(t *testing.T) {
	if _, err := SegmentGrid(samplesAt(0, 100), []int64{100, 100}, 0); err == nil {
		t.Fatal("zero target duration accepted")
	}
	if _, err := FrameDurations(nil); err == nil || err.Kind != clock.ErrInvalidLength {
		t.Fatal("empty timeline duration request accepted")
	}
}
