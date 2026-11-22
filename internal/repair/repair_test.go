package repair

import (
	"testing"

	"mediasync/internal/clock"
)

func buildFrames(raw []uint32) []*Frame {
	out := make([]*Frame, len(raw))
	for i, ts := range raw {
		out[i] = &Frame{
			Track:          "v",
			Representation: "v-hi",
			SegmentNumber:  i/3 + 1,
			SourceOffset:   int64(i * 100),
			IsKeyFrame:     i%5 == 0,
			RawTS:          ts,
			RawTimescale:   90000,
		}
	}
	return out
}

func TestRepairWrapPropagatesAndExpands(t *testing.T) {
	// 32-bit timestamps stepping by 90000 ticks across the wrap boundary.
	base := uint32(0xFFFFFFFF - 0x5A)
	raw := []uint32{
		base,
		base + 90000,
		base + 180000,
		base + 270000,
	}
	r := NewRepairer()
	frames := buildFrames(raw)
	for _, f := range frames {
		if err := r.RepairFrame(f, false, false); err != nil {
			t.Fatal(err)
		}
	}
	st, err := r.FinalizeTrack(frames)
	if err != nil {
		t.Fatal(err)
	}
	if st.Wraps < 1 {
		t.Fatalf("wraps = %d, want >=1", st.Wraps)
	}
	for i := 1; i < len(frames); i++ {
		if frames[i].CorrectedNS <= frames[i-1].CorrectedNS {
			t.Fatalf("post-wrap timestamps not increasing at %d: %v",
				i, frames[i].CorrectedNS)
		}
	}
}

func TestRepairDuplicateTimestampsNudged(t *testing.T) {
	raw := []uint32{0, 90000, 90000, 180000}
	r := NewRepairer()
	frames := buildFrames(raw)
	for _, f := range frames {
		if err := r.RepairFrame(f, false, false); err != nil {
			t.Fatal(err)
		}
	}
	st, err := r.FinalizeTrack(frames)
	if err != nil {
		t.Fatal(err)
	}
	if st.DuplicateTS != 1 {
		t.Fatalf("duplicates = %d, want 1", st.DuplicateTS)
	}
	if frames[2].CorrectedNS-frames[1].CorrectedNS != 1 {
		t.Fatal("duplicate must be nudged by exactly 1ns")
	}
}

func TestRepairMissingFramesPreserveVFRSpacing(t *testing.T) {
	// Gaps in frame numbering (missing frames) must not be re-gridded:
	// observed per-frame spacing survives repair.
	raw := []uint32{0, 30000, 90000, 120000, 180000} // mixed 1/30000-step gaps
	r := NewRepairer()
	frames := buildFrames(raw)
	for _, f := range frames {
		if err := r.RepairFrame(f, false, false); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := r.FinalizeTrack(frames)
	if st.VFRIntervalShifts < 3 {
		t.Fatalf("vfr shifts = %d, want >=3", st.VFRIntervalShifts)
	}
	for i := 1; i < len(frames); i++ {
		want := int64(raw[i]-raw[i-1]) * 1_000_000_000 / 90000
		got := frames[i].CorrectedNS - frames[i-1].CorrectedNS
		// Per-frame rational conversion may round by 1ns; the spacing
		// pattern itself must survive (never re-gridded to an even rate).
		d := got - want
		if d < 0 {
			d = -d
		}
		if d > 1 {
			t.Fatalf("frame %d spacing = %d, want %d (+/-1ns)", i, got, want)
		}
	}
	// The non-uniform gap pattern is preserved: both gap sizes occur.
	g0 := frames[1].CorrectedNS - frames[0].CorrectedNS
	g1 := frames[2].CorrectedNS - frames[1].CorrectedNS
	if g0 == g1 {
		t.Fatalf("VFR spacing was uniformised: %d", g0)
	}
}

func TestRepairDriftCorrection(t *testing.T) {
	var anchors []clock.Anchor
	for k := int64(0); k < 4; k++ {
		trueNS := k * 2_000_000_000
		obsNS := int64(float64(trueNS) / 1.0002)
		anchors = append(anchors, clock.Anchor{ObsNS: obsNS, TrueNS: trueNS})
	}
	r := NewRepairer()
	if err := r.FitDrift("v", anchors); err != nil {
		t.Fatal(err)
	}
	frames := buildFrames([]uint32{0, 90000, 180000})
	for _, f := range frames {
		if err := r.RepairFrame(f, false, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.FinalizeTrack(frames); err != nil {
		t.Fatal(err)
	}
	driftMarks := 0
	for _, f := range frames {
		for _, a := range f.Anomalies {
			if a.Kind == AnomalyDrift {
				driftMarks++
			}
		}
	}
	if driftMarks < 2 { // frame 0 happens to map near zero
		t.Fatalf("drift annotations = %d, want >=2", driftMarks)
	}
}

func TestRepairTruncatedAndDiscontinuityAnnotated(t *testing.T) {
	r := NewRepairer()
	frames := buildFrames([]uint32{0, 90000, 180000})
	if err := r.RepairFrame(frames[1], true, false); err != nil {
		t.Fatal(err)
	}
	if err := r.RepairFrame(frames[2], false, true); err != nil {
		t.Fatal(err)
	}
	if err := r.RepairFrame(frames[0], false, false); err != nil {
		t.Fatal(err)
	}
	st, err := r.FinalizeTrack(frames)
	if err != nil {
		t.Fatal(err)
	}
	if st.Truncated != 1 || st.Discontinuities != 1 {
		t.Fatalf("stats wrong: %+v", st)
	}
}

func TestRepairZeroTimescaleRejected(t *testing.T) {
	r := NewRepairer()
	f := &Frame{Track: "x", RawTimescale: 0}
	if err := r.RepairFrame(f, false, false); err == nil {
		t.Fatal("zero timescale frame must be rejected")
	}
}

func TestFinalizeEmptyRejected(t *testing.T) {
	r := NewRepairer()
	if _, err := r.FinalizeTrack(nil); err == nil {
		t.Fatal("empty track must be rejected")
	}
}
