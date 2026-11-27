package clock

import (
	"testing"

	"tsrepair/internal/mediaerr"
)

func TestPTSUnwrapAndWrap(t *testing.T) {
	var u PTSUnwrapper
	seq := []int64{100, 200, PTSMod - 10, 5, 6}
	want := []int64{100, 200, PTSMod - 10, PTSMod + 5, PTSMod + 6}
	for i, raw := range seq {
		got, _, err := u.Unwrap(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got != want[i] {
			t.Errorf("Unwrap(%d)=%d want %d", raw, got, want[i])
		}
	}
}

func TestPTSBackwardReported(t *testing.T) {
	var u PTSUnwrapper
	u.Unwrap(1000)
	if _, _, err := u.Unwrap(900); err == nil {
		t.Fatal("expected backward PTS error")
	}
}

func TestTimelineNoAnchorUnindexable(t *testing.T) {
	ec := &mediaerr.Collector{}
	tl := NewTimeline(0x100, TimelineConfig{}, ec)
	s, err := tl.AddSample(0, 0, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Indexable {
		t.Error("sample before anchor must be unindexable")
	}
	if ec.CountByCode(mediaerr.CodePTSNoAnchor) != 1 {
		t.Fatalf("errors: %v", ec.All())
	}
}

func TestTimelineAnchorWallTimeAndDuplicate(t *testing.T) {
	ec := &mediaerr.Collector{}
	tl := NewTimeline(0x100, TimelineConfig{Duplicates: DuplicateDrop}, ec)
	// Anchor: PTS 0 corresponds to PCR 27e6 ticks = 1 s wall time.
	if err := tl.SetPCRAnchor(0, 27_000_000); err != nil {
		t.Fatal(err)
	}
	// PTS 90000 = +1 s -> 2 s wall time.
	s, _ := tl.AddSample(90_000, 90_000, true, 188)
	if !s.Indexable || s.WallNanos != 2_000_000_000 {
		t.Errorf("sample=%+v", s)
	}
	// Duplicate PTS is dropped from indexing.
	s2, _ := tl.AddSample(90_000, 90_000, false, 376)
	if !s2.Duplicate || s2.Indexable {
		t.Errorf("duplicate handling wrong: %+v", s2)
	}
	if ec.CountByCode(mediaerr.CodePTSDuplicate) != 1 {
		t.Fatalf("errors: %v", ec.All())
	}
}

func TestTimelineVFRSpacingPreserved(t *testing.T) {
	ec := &mediaerr.Collector{}
	tl := NewTimeline(0x100, TimelineConfig{}, ec)
	tl.SetPCRAnchor(0, 0)
	// Non-uniform 90 kHz deltas: 3000, 6000, 3000 ticks.
	for _, pts := range []int64{0, 3000, 9000, 12000} {
		s, err := tl.AddSample(pts, pts, true, 0)
		if err != nil || !s.Indexable {
			t.Fatalf("pts=%d sample=%+v err=%v", pts, s, err)
		}
	}
	if !tl.IsVFR() {
		t.Error("expected VFR detection")
	}
	d0, ok := tl.IntervalNanos(0)
	if !ok || d0 != 33_333_333 { // 3000/90000 s
		t.Errorf("interval0=%d ok=%v", d0, ok)
	}
	d1, _ := tl.IntervalNanos(1)
	if d1 != 66_666_667 { // 6000/90000 s, truncated
		t.Errorf("interval1=%d want 66666667", d1)
	}
}

func TestTimelineCFRNotVFR(t *testing.T) {
	ec := &mediaerr.Collector{}
	tl := NewTimeline(0x100, TimelineConfig{}, ec)
	tl.SetPCRAnchor(0, 0)
	for i := int64(0); i < 4; i++ {
		tl.AddSample(i*3003, i*3003, true, 0)
	}
	if tl.IsVFR() {
		t.Error("uniform spacing must not be reported as VFR")
	}
}

func TestTimelineDTSAfterPTSUnindexable(t *testing.T) {
	ec := &mediaerr.Collector{}
	tl := NewTimeline(0x100, TimelineConfig{}, ec)
	tl.SetPCRAnchor(0, 0)
	s, _ := tl.AddSample(1000, 2000, true, 0)
	if s.Indexable {
		t.Error("DTS later than PTS must be unindexable")
	}
	if ec.CountByCode(mediaerr.CodePTSBackward) != 1 {
		t.Fatalf("errors: %v", ec.All())
	}
}
