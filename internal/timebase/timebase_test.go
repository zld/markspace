package timebase

import "testing"

func TestRationalConversionsAreExact(t *testing.T) {
	// 500 ms is 45,000 ticks at the 90 kHz event/video clock.
	got, err := Ms(500).Floor(EventRate)
	if err != nil {
		t.Fatal(err)
	}
	if got != 45_000 {
		t.Fatalf("500ms@90k = %d, want 45000", got)
	}

	// The same instant is exactly 24,000 samples at 48 kHz.
	got, err = Ms(500).Floor(AudioRate)
	if err != nil {
		t.Fatal(err)
	}

	// One second survives a 90k -> 48k -> 90k conversion without float drift.
	audio := Scale(90_000, EventRate, AudioRate)
	back := Scale(audio, AudioRate, EventRate)
	if audio != 48_000 || back != 90_000 {
		t.Fatalf("round trip = %d/%d", audio, back)
	}
}

func TestMulDivFloor(t *testing.T) {
	cases := []struct{ a, b, d, want int64 }{
		{1, 90_000, 1000, 90},
		{45_000, 1000, 90_000, 500},
		{12_345_678, 48_000, 90_000, 6_584_361},
		{-1, 1, 3, -1},
		{-2, 1, 3, -1}, // floor(-2/3) = -1
	}
	for _, tc := range cases {
		if got := MulDivFloor(tc.a, tc.b, tc.d); got != tc.want {
			t.Errorf("MulDivFloor(%d,%d,%d)=%d want %d", tc.a, tc.b, tc.d, got, tc.want)
		}
	}
}

func TestHalfOpenRanges(t *testing.T) {
	r := Range{Start: 10, End: 20}
	for _, v := range []int64{10, 15, 19} {
		if !r.Contains(v) {
			t.Errorf("expected %d in %v", v, r)
		}
	}
	for _, v := range []int64{9, 20, 21} {
		if r.Contains(v) {
			t.Errorf("did not expect %d in %v", v, r)
		}
	}

	hit, ok := Intersect(Range{0, 12}, Range{10, 20})
	if !ok || hit != (Range{10, 12}) {
		t.Fatalf("intersection = %v,%v", hit, ok)
	}
	if _, ok := Intersect(Range{0, 10}, Range{10, 20}); ok {
		t.Fatal("touching half-open ranges must not intersect")
	}
}

func TestAlignError(t *testing.T) {
	if d := AlignError(45_000, 46_800, EventRate); d != 20 {
		t.Fatalf("alignment error = %dms, want 20", d)
	}
}
