package cmaf

import (
	"math"
	"testing"
	"time"

	"replay/internal/model"
)

func frag(seq uint32, tfdt, dur uint64) *Fragment {
	return &Fragment{Path: "seg.m4s", Seq: seq, Tfdt: tfdt, VideoDur: dur, HasVideo: true}
}

func build(frags []*Fragment) (*Timeline, *model.Report) {
	rep := &model.Report{}
	return BuildTimeline(frags, testInit(), rep), rep
}

func TestTimelineContinuous(t *testing.T) {
	// 90kHz video: two 2s fragments.
	tl, rep := build([]*Fragment{frag(1, 0, 180000), frag(2, 180000, 180000)})
	if tl.SeqGaps != 0 || tl.Duplicates != 0 || tl.Regressions != 0 {
		t.Fatalf("unexpected damage: %+v", tl)
	}
	if len(tl.Segments) != 2 || len(tl.Missing) != 0 {
		t.Fatalf("bad segments: %+v", tl)
	}
	if tl.End != 4*time.Second {
		t.Fatalf("End = %v, want 4s", tl.End)
	}
	if tl.Segments[1].Start != 2*time.Second {
		t.Fatalf("segment 1 start = %v, want 2s", tl.Segments[1].Start)
	}
	if rep.Count(model.CodeMFHDGap) != 0 {
		t.Fatal("no gap expected")
	}
}

func TestTimelineSeqGapWithMediaHole(t *testing.T) {
	// seq 2 missing and its media (2s..4s) gone: tfdt jumps.
	tl, rep := build([]*Fragment{frag(1, 0, 180000), frag(3, 360000, 180000)})
	if tl.SeqGaps != 1 {
		t.Fatalf("SeqGaps = %d, want 1", tl.SeqGaps)
	}
	if len(tl.Missing) != 1 || tl.Missing[0].Start != 2*time.Second || tl.Missing[0].End != 4*time.Second {
		t.Fatalf("bad missing interval: %+v", tl.Missing)
	}
	if rep.Count(model.CodeMFHDGap) != 1 || rep.Count(model.CodeMissingMedia) != 1 {
		t.Fatalf("bad counts: %+v", rep.Counts())
	}
}

func TestTimelineDuplicateDropped(t *testing.T) {
	tl, rep := build([]*Fragment{frag(1, 0, 180000), frag(1, 0, 180000), frag(2, 180000, 180000)})
	if tl.Duplicates != 1 {
		t.Fatalf("Duplicates = %d, want 1", tl.Duplicates)
	}
	if len(tl.Segments) != 2 {
		t.Fatalf("duplicate counted in segments: %+v", tl.Segments)
	}
	if tl.End != 4*time.Second {
		t.Fatalf("duplicate counted in duration: End = %v", tl.End)
	}
	if rep.Count(model.CodeMFHDDup) != 1 {
		t.Fatal("expected MFHD_DUP issue")
	}
}

func TestTimelineTfdtRegression(t *testing.T) {
	// seq 2 claims tfdt 0 again: regression, span marked missing.
	tl, rep := build([]*Fragment{frag(1, 0, 180000), frag(2, 0, 180000), frag(3, 180000, 180000)})
	if tl.Regressions != 1 {
		t.Fatalf("Regressions = %d, want 1", tl.Regressions)
	}
	if len(tl.Segments) != 2 {
		t.Fatalf("regressed fragment must not be placed: %+v", tl.Segments)
	}
	if len(tl.Missing) != 1 || tl.Missing[0].Start != 0 || tl.Missing[0].End != 2*time.Second {
		t.Fatalf("bad missing interval: %+v", tl.Missing)
	}
	if rep.Count(model.CodeTfdtRegression) != 1 {
		t.Fatal("expected TFDT_REGRESSION issue")
	}
}

func TestTimelineOutOfOrderFiles(t *testing.T) {
	// File order scrambled; sequence numbers define the real order.
	tl, _ := build([]*Fragment{frag(3, 360000, 180000), frag(1, 0, 180000), frag(2, 180000, 180000)})
	if len(tl.Segments) != 3 || len(tl.Missing) != 0 {
		t.Fatalf("bad timeline: %+v", tl)
	}
	if tl.Segments[0].Start != 0 || tl.Segments[2].Start != 4*time.Second {
		t.Fatalf("segments not ordered by media time: %+v", tl.Segments)
	}
}

func TestTimelineDamagedFragment(t *testing.T) {
	d := frag(2, 180000, 180000)
	d.Damaged = true
	tl, _ := build([]*Fragment{frag(1, 0, 180000), d, frag(3, 360000, 180000)})
	if len(tl.Segments) != 2 {
		t.Fatalf("damaged fragment must not be a segment: %+v", tl.Segments)
	}
	if len(tl.Missing) != 1 || tl.Missing[0].Start != 2*time.Second {
		t.Fatalf("bad missing: %+v", tl.Missing)
	}
	if got := tl.SegmentsIn(0, 3*time.Second); len(got) != 1 {
		t.Fatalf("SegmentsIn = %d, want 1", len(got))
	}
	if got := tl.MissingIn(time.Second, 3*time.Second); len(got) != 1 {
		t.Fatalf("MissingIn = %d, want 1", len(got))
	}
}

func TestTimelineRTPBase(t *testing.T) {
	tl, _ := build([]*Fragment{frag(1, 0, 180000), frag(2, 180000, 180000)})
	delta := tl.Segments[1].RTPStart - tl.Segments[0].RTPStart
	if delta != 180000 {
		t.Fatalf("RTP delta = %d, want 180000 (2s at 90kHz)", delta)
	}
}

func TestTimelineTimestampOverflowNoWrap(t *testing.T) {
	// Tfdt near uint64 max must not wrap Tfdt+Dur into a small expected time;
	// the overflow is reported through the shared report instead.
	_, rep := build([]*Fragment{frag(1, math.MaxUint64-10, 180000)})
	if !rep.HasFatal() {
		t.Fatal("expected a fatal overflow report for an unrepresentable timestamp")
	}
	if rep.Count(model.CodeMissingMedia) == 0 {
		t.Fatal("expected an overflow-backed MISSING_MEDIA issue")
	}
}

func TestSatAdd(t *testing.T) {
	if got := satAdd(10, 20); got != 30 {
		t.Fatalf("satAdd = %d, want 30", got)
	}
	if got := satAdd(math.MaxUint64, 5); got != math.MaxUint64 {
		t.Fatalf("satAdd overflow = %d, want MaxUint64 saturation", got)
	}
}
