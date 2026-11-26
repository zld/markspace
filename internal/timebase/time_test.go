package timebase

import "testing"

func TestRationalScaleToNano(t *testing.T) {
	// RTP 48 kHz clock: 960 ticks is exactly 20 ms.
	rtp48 := Rational{Num: 1, Den: 48_000}
	got, err := rtp48.ScaleToNano(960)
	if err != nil {
		t.Fatal(err)
	}
	if want := Milliseconds(20); got != want {
		t.Fatalf("ScaleToNano(960) = %d, want %d", got, want)
	}

	// Matroska default timecode scale is 1 ms per tick.
	ms := Rational{Num: 1_000_000, Den: 1_000_000_000}
	got, err = ms.ScaleToNano(1234)
	if err != nil {
		t.Fatal(err)
	}
	if want := Milliseconds(1234); got != want {
		t.Fatalf("ms scale = %d, want %d", got, want)
	}

	if _, err := (Rational{}).ScaleToNano(1); err == nil {
		t.Fatal("expected invalid rational error")
	}
}

func TestIntervalHalfOpenAndMerge(t *testing.T) {
	a := Interval{Lo: 0, Hi: 5}
	b := Interval{Lo: 4, Hi: 8}
	c := Interval{Lo: 8, Hi: 10} // adjacent to b, not overlapping

	if !a.Intersects(b) {
		t.Fatal("expected [0,5) and [4,8) to overlap")
	}
	if b.Intersects(c) {
		t.Fatal("adjacent half-open intervals must not intersect")
	}
	got := MergeOverlapping([]Interval{c, b, a})
	want := []Interval{{Lo: 0, Hi: 8}, {Lo: 8, Hi: 10}}
	if len(got) != len(want) {
		t.Fatalf("merged len = %d, want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("merged[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
