package media

import (
	"errors"
	"testing"
)

func TestFractionTicks(t *testing.T) {
	cases := []struct {
		num, den int64
		want     Ticks
	}{
		{0, 1000, 0},
		{1, 1, Second},
		{1, 1000, Millisecond},
		{1, 1_000_000_000, 1},
		{3, 1_000_000_000, 3},
		{1, 2, 500 * Millisecond},
		{5, 3, 1_666_666_666}, // floor
		{-5, 3, -1_666_666_666},
		{48000, 48000, Second},
		{44100, 44100, Second},
	}
	for _, c := range cases {
		got, err := FractionTicks(c.num, c.den)
		if err != nil {
			t.Fatalf("FractionTicks(%d,%d): %v", c.num, c.den, err)
		}
		if got != c.want {
			t.Errorf("FractionTicks(%d,%d)=%d want %d", c.num, c.den, got, c.want)
		}
	}
}

func TestFractionTicksRejectsBadDenominator(t *testing.T) {
	for _, den := range []int64{0, -1} {
		_, err := FractionTicks(1, den)
		if !IsClass(err, ClassInvalidTimebase) {
			t.Fatalf("den=%d err=%v, want invalid_timebase", den, err)
		}
		e, ok := AsError(err)
		if !ok || e.Stage != StageTimeline || e.Offset != -1 {
			t.Fatalf("unexpected error shape: %#v", e)
		}
	}
}

func TestFractionTicksOverflow(t *testing.T) {
	_, err := FractionTicks(1<<62, 1)
	if !IsClass(err, ClassOutOfRange) {
		t.Fatalf("err=%v, want out_of_range", err)
	}
}

func TestSampleTicksAndInverse(t *testing.T) {
	// 48000 Hz: one AAC-style frame of 1024 samples.
	got, err := SampleTicks(1024, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if want := Ticks(21_333_333); got != want {
		t.Fatalf("SampleTicks=%d want %d", got, want)
	}
	// Aligning back to samples must land on or after the requested instant.
	s, err := SamplesAtTicks(got, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if s != 1024 {
		t.Fatalf("SamplesAtTicks=%d want 1024", s)
	}
	s, err = SamplesAtTicks(1, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if s != 1 {
		t.Fatalf("ceil alignment got %d want 1", s)
	}
}

func TestSampleTicksZeroTimescale(t *testing.T) {
	_, err := SampleTicks(1, 0)
	if !IsClass(err, ClassInvalidTimebase) {
		t.Fatalf("err=%v want invalid_timebase", err)
	}
}

func TestSamplesAtTicksNegative(t *testing.T) {
	_, err := SamplesAtTicks(-1, 48000)
	if !IsClass(err, ClassTimeRegression) {
		t.Fatalf("err=%v want time_regression", err)
	}
}

func TestWindowIntersect(t *testing.T) {
	a := Window{Start: 0, End: 100}
	b := Window{Start: 50, End: 200}
	got, ok := a.Intersect(b)
	if !ok || got.Start != 50 || got.End != 100 {
		t.Fatalf("intersect=%+v ok=%v", got, ok)
	}
	if _, ok := a.Intersect(Window{Start: 100, End: 200}); ok {
		t.Fatal("touching windows must not overlap")
	}
	if !a.Overlaps(b) {
		t.Fatal("expected overlap")
	}
	if (Window{Start: 0, End: 0}).Contains(0) {
		t.Fatal("half-open window must exclude its end")
	}
}

func TestWindowValidate(t *testing.T) {
	if err := (Window{Start: -1, End: 2}).Validate(); !IsClass(err, ClassTimeRegression) {
		t.Fatalf("negative start err=%v", err)
	}
	if err := (Window{Start: 5, End: 2}).Validate(); !IsClass(err, ClassTimeRegression) {
		t.Fatalf("inverted err=%v", err)
	}
	if err := (Window{Start: 0, End: 2}).Validate(); err != nil {
		t.Fatalf("valid window rejected: %v", err)
	}
}

func TestFirstOffset(t *testing.T) {
	errs := []*Error{
		NewError(ClassCorruptInput, StageParse, "v0", 120, "bad box"),
		NewError(ClassTruncatedInput, StageParse, "a0", 40, "short"),
		NewError(ClassCorruptInput, StageParse, "v0", -1, "no offset"),
	}
	off, first, ok := FirstOffset(errs)
	if !ok || off != 40 || first.Track != "a0" {
		t.Fatalf("FirstOffset=%d %+v %v", off, first, ok)
	}
	if _, _, ok := FirstOffset(nil); ok {
		t.Fatal("no offsets expected")
	}
}

func TestErrorUnwrap(t *testing.T) {
	cause := errors.New("boom")
	err := WrapError(ClassCorruptInput, StageParse, "v0", 9, "wrap", cause)
	if !errors.Is(err, cause) {
		t.Fatal("cause not unwrapped")
	}
	got, ok := AsError(err)
	if !ok || got.Class != ClassCorruptInput || got.Offset != 9 {
		t.Fatalf("AsError=%+v", got)
	}
	if got.WithTrack("x").Track != "x" || got.Track != "v0" {
		t.Fatal("WithTrack must copy")
	}
}
