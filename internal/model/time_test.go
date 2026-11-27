package model

import (
	"errors"
	"math"
	"testing"
)

func TestRationalNanosExact(t *testing.T) {
	cases := []struct {
		num, den int64
		want     Nanos
	}{
		{1, 2, 500_000_000},
		{1, 3, 333_333_333},
		{2, 3, 666_666_667},
		{-1, 3, -333_333_333},
		{0, 7, 0},
		{90_000, 90_000, 1_000_000_000},
	}
	for _, c := range cases {
		r, err := NewRational(c.num, c.den)
		if err != nil {
			t.Fatalf("NewRational(%d,%d): %v", c.num, c.den, err)
		}
		got, err := r.Nanos()
		if err != nil {
			t.Fatalf("Nanos(%d/%d): %v", c.num, c.den, err)
		}
		if got != c.want {
			t.Errorf("%d/%d nanos = %d, want %d", c.num, c.den, got, c.want)
		}
	}
}

func TestRationalNormalises(t *testing.T) {
	r, err := NewRational(600, -1200)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != -1 || r.Den != 2 {
		t.Fatalf("got %s, want -1/2", r)
	}
	if _, err := NewRational(1, 0); !errors.Is(err, ErrZeroDenominator) {
		t.Fatalf("want zero denominator error, got %v", err)
	}
}

func TestRationalNoFloatDrift(t *testing.T) {
	// Accumulating 1/1000 s one thousand times must land exactly on 1 s.
	step, err := NewTime(1, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := step.Nanos()
	if err != nil {
		t.Fatal(err)
	}
	var total Nanos
	for i := 0; i < 1000; i++ {
		total += n
	}
	if total != Nanos(NanosPerSecond) {
		t.Fatalf("accumulated %d nanos, want exactly %d", total, NanosPerSecond)
	}
	// Rational addition stays exact too.
	unit, _ := NewRational(1, 1000)
	acc := Rational{Num: 0, Den: 1}
	for i := 0; i < 1000; i++ {
		acc, err = acc.Add(unit)
		if err != nil {
			t.Fatal(err)
		}
	}
	if acc.Num != 1 || acc.Den != 1 {
		t.Fatalf("accumulated %s, want 1/1", acc)
	}
}

func TestTimeShiftedNanos(t *testing.T) {
	tm, err := NewTime(25_000, 25_000, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := tm.Nanos()
	if err != nil {
		t.Fatal(err)
	}
	if plain != 1_000_000_000 {
		t.Fatalf("plain = %d", plain)
	}
	shifted, err := tm.ShiftedNanos()
	if err != nil {
		t.Fatal(err)
	}
	if shifted != 960_000_000 {
		t.Fatalf("shifted = %d, want 960ms", shifted)
	}
}

func TestTimeSub(t *testing.T) {
	a, _ := NewTime(48_000, 48_000, 0)
	b, _ := NewTime(36_000, 48_000, 0)
	d, err := a.Sub(b)
	if err != nil {
		t.Fatal(err)
	}
	if d != 250_000_000 {
		t.Fatalf("sub = %d, want 250ms", d)
	}
	c, _ := NewTime(1, 1000, 0)
	if _, err := a.Sub(c); err == nil {
		t.Fatal("expected timescale mismatch error")
	}
}

func TestSpanOverlapAndIntersect(t *testing.T) {
	a, _ := NewSpan(0, 100)
	b, _ := NewSpan(50, 150)
	c, _ := NewSpan(100, 200)
	if !a.Overlaps(b) || a.Overlaps(c) {
		t.Fatal("overlap detection wrong")
	}
	in, ok := a.Intersect(b)
	if !ok || in.Start != 50 || in.End != 100 {
		t.Fatalf("intersect = %v %v", in, ok)
	}
	if _, err := NewSpan(10, 5); err == nil {
		t.Fatal("expected invalid span error")
	}
}

func TestOverflowGuards(t *testing.T) {
	r := Rational{Num: math.MaxInt64, Den: 1}
	if _, err := r.Nanos(); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected overflow, got %v", err)
	}
	tm, _ := NewTime(math.MaxInt64, 1, 0)
	if _, err := tm.Nanos(); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected overflow, got %v", err)
	}
	if _, err := NewTime(1, 0, 0); !errors.Is(err, ErrZeroTimescale) {
		t.Fatalf("expected zero timescale, got %v", err)
	}
}

func TestRationalCmp(t *testing.T) {
	a, _ := NewRational(1, 3)
	b, _ := NewRational(2, 6)
	if a.Cmp(b) != 0 {
		t.Fatal("1/3 should equal 2/6")
	}
	c, _ := NewRational(-1, 3)
	if c.Cmp(a) >= 0 {
		t.Fatal("-1/3 should sort before 1/3")
	}
	if a.Cmp(c) <= 0 {
		t.Fatal("1/3 should sort after -1/3")
	}
}

func TestErrorCarriesContext(t *testing.T) {
	base := errors.New("unexpected eof")
	err := New(KindCorrupt, StageMP4Scan, "cam0.mp4", "read_box", base).
		WithVersion("v2").WithTrack("video").WithOffset(4096)
	var me *Error
	if !errors.As(err, &me) {
		t.Fatal("structured error lost")
	}
	if me.Asset != "cam0.mp4" || me.Stage != StageMP4Scan || !me.HasOff || me.Offset != 4096 {
		t.Fatalf("context lost: %+v", me)
	}
	if !errors.Is(err, base) {
		t.Fatal("cause not unwrappable")
	}
	if err.ExitCode() != 2 {
		t.Fatalf("corrupt exit code = %d, want 2", err.ExitCode())
	}
	if ExitCodeOf(errors.New("plain")) != 1 {
		t.Fatal("plain error should exit 1")
	}
	// Wrap preserves an existing structured error unchanged.
	wrapped := Wrap(err, KindInternal, StageMerge, "x", "y")
	if wrapped != err {
		t.Fatal("structured error should not be re-wrapped")
	}
}

func TestMultiErrorUnwrap(t *testing.T) {
	m := &MultiError{Kind: KindConflict, Label: "conflicts", Errors: []*Error{
		New(KindConflict, StageMerge, "a", "cmp", nil),
	}}
	if errs := m.Unwrap(); len(errs) != 1 {
		t.Fatalf("unwrapped %d", len(errs))
	}
	if m.ExitCode() != 2 {
		t.Fatalf("exit = %d", m.ExitCode())
	}
}
