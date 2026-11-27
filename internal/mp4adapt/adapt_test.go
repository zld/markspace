package mp4adapt

import (
	"testing"

	"example.com/continuity/internal/manifest"
)

func TestRenderAndReadBack(t *testing.T) {
	segments := []manifest.SegmentRecord{
		{FrameStart: 0, FrameEnd: 50, StartNS: 0, EndNS: 2_000_000_000},
		{FrameStart: 50, FrameEnd: 72, StartNS: 2_000_000_000, EndNS: 3_080_000_000},
	}
	data, err := Render(segments)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("empty MP4 rendered")
	}
	tl, err := ReadBack(data)
	if err != nil {
		t.Fatal(err)
	}
	if tl.Timescale != 90000 {
		t.Fatalf("timescale=%d", tl.Timescale)
	}
	diff, err := AlignmentErrorNS(segments, tl)
	if err != nil {
		t.Fatal(err)
	}
	if diff > 1 {
		t.Fatalf("alignment error %d ns exceeds 1 ns (mp4=%d want=%d)",
			diff, tl.DurationNS, segments[1].EndNS)
	}
}

func TestRenderRejectsEmpty(t *testing.T) {
	if _, err := Render(nil); err == nil {
		t.Fatal("empty segment set accepted")
	}
}

func TestRoundTripMultipleDurations(t *testing.T) {
	for _, endNS := range []int64{40_000_000, 1_000_000_000, 4_800_000_000} {
		segs := []manifest.SegmentRecord{{EndNS: endNS}}
		data, err := Render(segs)
		if err != nil {
			t.Fatalf("render %d: %v", endNS, err)
		}
		tl, err := ReadBack(data)
		if err != nil {
			t.Fatalf("readback %d: %v", endNS, err)
		}
		diff, _ := AlignmentErrorNS(segs, tl)
		if diff > 1 {
			t.Fatalf("endNS=%d alignment diff=%d", endNS, diff)
		}
	}
}
