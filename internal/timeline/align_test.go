package timeline

import (
	"math"
	"testing"

	"dashforge/internal/model"
)

func rendition(id string, bw int, offset int64, segs ...model.Segment) model.Rendition {
	return model.Rendition{
		ID: id, Width: 320, Height: 180, Bandwidth: bw, Codec: "avc1",
		FrameRate: "30/1", StartOffsetMs: offset, SegmentDurationMs: 4000,
		Media: "m/$Number$.m4s", SegmentRoot: "root", Segments: segs,
	}
}

func seg(n int, start, dur int64) model.Segment {
	return model.Segment{Number: n, StartMs: start, DurationMs: dur}
}

func TestNormalizeExactScaleAndOrdering(t *testing.T) {
	meta := &model.Metadata{Renditions: []model.Rendition{
		rendition("hi", 2_000_000, 0, seg(1, 0, 4000), seg(2, 4000, 2000)),
		rendition("lo", 500_000, 0, seg(1, 0, 4000)),
	}}
	man, report, err := Normalize(meta, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Deviations) != 0 {
		t.Fatalf("unexpected deviations: %+v", report.Deviations)
	}
	if got := man.Renditions[0].ID; got != "lo" || man.Renditions[1].ID != "hi" {
		t.Fatalf("renditions not sorted by bandwidth: %s, %s", got, man.Renditions[1].ID)
	}
	hi := man.Renditions[1]
	if hi.Segments[0].Start != 0 || hi.Segments[0].Duration != 360000 {
		t.Fatalf("unexpected first segment: %+v", hi.Segments[0])
	}
	if hi.Segments[1].Duration != 180000 {
		t.Fatalf("short segment not preserved: %+v", hi.Segments[1])
	}
	if hi.Duration != 540000 {
		t.Fatalf("duration = %d, want 540000", hi.Duration)
	}
	if man.PeriodDuration() != 540000 {
		t.Fatalf("period duration = %d", man.PeriodDuration())
	}
	// 4000ms run then a shorter final fragment -> two timeline runs.
	if len(hi.Timeline) != 2 || hi.Timeline[0].Repeat != 0 {
		t.Fatalf("unexpected timeline: %+v", hi.Timeline)
	}
}

func TestNormalizeRepeatsAndGaps(t *testing.T) {
	meta := &model.Metadata{Renditions: []model.Rendition{
		rendition("r", 100, 0,
			seg(3, 8000, 4000), seg(1, 0, 4000), seg(2, 4000, 4000)),
	}}
	man, _, err := Normalize(meta, 1000)
	if err != nil {
		t.Fatal(err)
	}
	rm := man.Renditions[0]
	// Out-of-order input must be renumbered by segment number on the timeline.
	if rm.Segments[0].Number != 1 || rm.Segments[1].Number != 2 || rm.Segments[2].Number != 3 {
		t.Fatalf("segments not sorted: %+v", rm.Segments)
	}
	if len(rm.Timeline) != 1 || rm.Timeline[0].Repeat != 2 {
		t.Fatalf("expected one run r=2, got %+v", rm.Timeline)
	}

	gapped := &model.Metadata{Renditions: []model.Rendition{
		rendition("g", 100, 0, seg(1, 0, 4000), seg(2, 9000, 4000)),
	}}
	gm, _, err := Normalize(gapped, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(gm.Renditions[0].Timeline) != 2 {
		t.Fatalf("gap must open a new timeline run: %+v", gm.Renditions[0].Timeline)
	}
}

func TestNormalizeOffsetAndDeviation(t *testing.T) {
	// timescale 29970: 1ms maps to 29.97 ticks, so boundaries do not divide.
	meta := &model.Metadata{Renditions: []model.Rendition{
		rendition("r", 100, 1, seg(1, 0, 1), seg(2, 1, 1)),
	}}
	man, report, err := Normalize(meta, 29970)
	if err != nil {
		t.Fatal(err)
	}
	rm := man.Renditions[0]
	if rm.StartOffset != 30 { // round(29.97)
		t.Fatalf("offset = %d, want 30", rm.StartOffset)
	}
	if rm.Segments[0].Start != 30 || rm.Segments[0].Duration != 30 {
		t.Fatalf("unexpected segment: %+v", rm.Segments[0])
	}
	if len(report.Deviations) == 0 {
		t.Fatal("expected rounding deviations to be reported")
	}
	for _, d := range report.Deviations {
		if d.ErrorMs <= 0 || math.Abs(d.ErrorMs) > 0.002 {
			t.Fatalf("implausible deviation: %+v", d)
		}
	}
}

func TestNormalizeDuplicateBandwidthRejected(t *testing.T) {
	meta := &model.Metadata{Renditions: []model.Rendition{
		rendition("a", 100, 0, seg(1, 0, 1)),
		rendition("b", 100, 0, seg(1, 0, 1)),
	}}
	if _, _, err := Normalize(meta, 1000); err == nil {
		t.Fatal("expected duplicate bandwidth error")
	}
	if _, _, err := Normalize(meta, 0); err == nil {
		t.Fatal("expected non-positive timescale error")
	}
}

func TestScaleRounding(t *testing.T) {
	if v, err := scale(4000, 90000); v != 360000 || err != 0 {
		t.Fatalf("exact scale failed: v=%d err=%v", v, err)
	}
	v, _ := scale(1, 29970)
	if v != 30 {
		t.Fatalf("rounded scale = %d, want 30", v)
	}
}
