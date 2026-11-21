package analysis

import "testing"

func TestBuildRanges(t *testing.T) {
	ranges, err := BuildRanges(200, 96)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ start, count int }{
		{0, 96}, {96, 96}, {192, 8},
	}
	if len(ranges) != len(want) {
		t.Fatalf("got %d ranges, want %d", len(ranges), len(want))
	}
	var total int
	for i, r := range ranges {
		if r.StartFrame != want[i].start || r.FrameCount != want[i].count {
			t.Errorf("range %d = (%d,%d), want (%d,%d)",
				i, r.StartFrame, r.FrameCount, want[i].start, want[i].count)
		}
		if r.Index != i {
			t.Errorf("range index %d != position %d", r.Index, i)
		}
		total += r.FrameCount
	}
	if total != 200 {
		t.Errorf("ranges cover %d frames, want 200 (tail must be included)", total)
	}
}

func TestBuildRangesExactMultiple(t *testing.T) {
	ranges, err := BuildRanges(192, 96)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 2 || ranges[1].FrameCount != 96 {
		t.Errorf("exact multiples must not create an empty tail: %+v", ranges)
	}
}

func TestBuildRangesRejectsBadInput(t *testing.T) {
	if _, err := BuildRanges(0, 96); err == nil {
		t.Error("zero frames should error")
	}
	if _, err := BuildRanges(100, 0); err == nil {
		t.Error("zero segment length should error")
	}
}

func TestGradientAndDiff(t *testing.T) {
	w, h := 4, 4
	flat := make([]byte, w*h)
	for i := range flat {
		flat[i] = 100
	}
	if g := meanGradient(flat, w, h); g != 0 {
		t.Errorf("flat frame gradient = %f, want 0", g)
	}
	textured := make([]byte, w*h)
	for i := range textured {
		if i%2 == 0 {
			textured[i] = 200
		}
	}
	if g := meanGradient(textured, w, h); g <= 0 {
		t.Errorf("textured frame gradient should be positive, got %f", g)
	}
	if d := meanAbsDiff(flat, flat); d != 0 {
		t.Errorf("identical frame diff = %f, want 0", d)
	}
}
