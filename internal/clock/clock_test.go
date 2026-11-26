package clock

import (
	"errors"
	"testing"
)

func TestMapExactRational(t *testing.T) {
	cases := []struct {
		value int64
		base  Rational
		want  Ticks
	}{
		{1, Frame24fps, 3750},
		{12, Frame24fps, 45000},
		{48000, Timebase48k, 90000},
		{180000, Timebase90k, 180000},
	}
	for _, tc := range cases {
		got, err := Map("trk", "test", tc.value, tc.value, tc.base)
		if err != nil {
			t.Fatalf("Map(%d): %v", tc.value, err)
		}
		if got.Ticks != tc.want || !got.Aligned() {
			t.Fatalf("Map(%d)=%d aligned=%v, want %d", tc.value, got.Ticks, got.Aligned(), tc.want)
		}
	}
}

func TestMapSubTickRemainderWithoutFloat(t *testing.T) {
	// One 48 kHz sample is 15/8 90 kHz ticks.
	m, err := Map("audio", "rtp", 7, 1, Timebase48k)
	if err != nil {
		t.Fatal(err)
	}
	if m.Ticks != 1 || m.RemNum != 7 || m.RemDen != 8 {
		t.Fatalf("unexpected mapping %+v", m)
	}
	var le *Error
	if !errors.As(Wrap("audio", "align", 42, errors.New("boom")), &le) {
		t.Fatal("located error must support errors.As")
	}
	if le.Offset != 42 || le.Track != "audio" || le.Stage != "align" {
		t.Fatalf("bad location %+v", le)
	}
}

func TestRangeLeftClosedRightOpenAndDuration(t *testing.T) {
	r := Range{From: 45000, To: 135000}
	if err := r.Validate(180000); err != nil {
		t.Fatal(err)
	}
	if err := (Range{From: 180000, To: 180001}).Validate(180000); err == nil {
		t.Fatal("end beyond duration must fail")
	}
	if err := (Range{From: 10, To: 10}).Validate(180000); err == nil {
		t.Fatal("empty interval must fail")
	}
}
