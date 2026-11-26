package timebase

import (
	"errors"
	"math"
	"testing"
)

func TestNewRatNormalizes(t *testing.T) {
	r, err := NewRat(6, 9)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num() != 2 || r.Den() != 3 {
		t.Fatalf("got %d/%d want 2/3", r.Num(), r.Den())
	}
	r2, err := NewRat(-4, -8) // negative/negative normalizes to positive 1/2
	if err != nil || r2.Num() != 1 || r2.Den() != 2 {
		t.Fatalf("negative normalization failed: %+v err=%v", r2, err)
	}
	r3, err := NewRat(4, -8) // positive/negative normalizes to -1/2
	if err != nil || r3.Num() != -1 || r3.Den() != 2 {
		t.Fatalf("sign flip failed: %+v err=%v", r3, err)
	}
	if _, err := NewRat(1, 0); !errors.Is(err, errZeroDenominator) {
		t.Fatalf("want zero denominator error, got %v", err)
	}
}

func TestRatArithmetic(t *testing.T) {
	a := MustNewRat(1, 3)
	b := MustNewRat(1, 6)
	sum, err := a.Add(b)
	if err != nil || sum.Num() != 1 || sum.Den() != 2 {
		t.Fatalf("1/3+1/6 = %v err=%v", sum, err)
	}
	diff, err := a.Sub(b)
	if err != nil || diff.Num() != 1 || diff.Den() != 6 {
		t.Fatalf("1/3-1/6 = %v err=%v", diff, err)
	}
	prod, err := a.Mul(b)
	if err != nil || prod.Num() != 1 || prod.Den() != 18 {
		t.Fatalf("1/3*1/6 = %v err=%v", prod, err)
	}
	if a.Cmp(b) <= 0 {
		t.Fatal("1/3 must be greater than 1/6")
	}
	if MustNewRat(2, 4).Cmp(MustNewRat(1, 2)) != 0 {
		t.Fatal("equivalent fractions must compare equal")
	}
}

func TestRoundAndFloor(t *testing.T) {
	cases := []struct {
		num, den int64
		round    int64
		floor    int64
	}{
		{5, 2, 3, 2},
		{4, 2, 2, 2},
		{-5, 2, -3, -3},
		{1, 3, 0, 0},
		{-1, 3, 0, -1},
		{7, 2, 4, 3},
	}
	for _, c := range cases {
		r := MustNewRat(c.num, c.den)
		got, err := r.Round()
		if err != nil || got != c.round {
			t.Errorf("Round(%d/%d)=%d err=%v, want %d", c.num, c.den, got, err, c.round)
		}
		if f := r.Floor(); f != c.floor {
			t.Errorf("Floor(%d/%d)=%d, want %d", c.num, c.den, f, c.floor)
		}
	}
}

func TestSharedMsConversionsRoundTrip(t *testing.T) {
	// 3003 media ticks at 90000 Hz = 33.3666... ms -> 33 ms.
	ms, err := ToSharedMs(3003, 90000)
	if err != nil || ms != 33 {
		t.Fatalf("ToSharedMs = %d, err=%v, want 33", ms, err)
	}
	// 1000 ms at 90000 Hz = 90000 ticks exactly.
	ticks, err := FromSharedMs(1000, 90000)
	if err != nil || ticks != 90000 {
		t.Fatalf("FromSharedMs = %d, err=%v, want 90000", ticks, err)
	}
	// Round trip on a whole-millisecond boundary.
	for _, msIn := range []int64{0, 1, 33, 1000, 60000} {
		tk, err := FromSharedMs(msIn, 90000)
		if err != nil {
			t.Fatal(err)
		}
		out, err := ToSharedMs(tk, 90000)
		if err != nil || out != msIn {
			t.Fatalf("round trip %dms -> %d ticks -> %dms err=%v", msIn, tk, out, err)
		}
	}
}

func TestScaleExactness(t *testing.T) {
	// 24000 ticks at 90k Hz expressed in 1k Hz = 266.666...
	r, err := Scale(24000, 90000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num() != 800 || r.Den() != 3 {
		t.Fatalf("got %d/%d want 800/3", r.Num(), r.Den())
	}
	if _, err := Scale(1, 0, 1000); !errors.Is(err, errZeroDenominator) {
		t.Fatalf("want zero denominator, got %v", err)
	}
}

func TestWithinOneTick(t *testing.T) {
	// Values are in seconds; one shared tick is 1 ms = 1/1000 s.
	// 33.3ms vs 33.9ms differ by 0.6 ms <= 1 ms.
	a := MustNewRat(333, 10000)
	b := MustNewRat(339, 10000)
	ok, err := WithinOneTick(a, b)
	if err != nil || !ok {
		t.Fatalf("0.6ms divergence must be within one tick: ok=%v err=%v", ok, err)
	}
	c := MustNewRat(35, 1000) // 35 ms vs 33.3 ms -> 1.7 ms
	ok, err = WithinOneTick(a, c)
	if err != nil || ok {
		t.Fatalf("1.7ms divergence must exceed one tick: ok=%v", ok)
	}
}

func TestOverflowGuards(t *testing.T) {
	if _, err := mul64(math.MaxInt64, 2); !errors.Is(err, errOverflow) {
		t.Fatalf("mul overflow not detected: %v", err)
	}
	if _, err := add64(math.MaxInt64, 1); !errors.Is(err, errOverflow) {
		t.Fatalf("add overflow not detected: %v", err)
	}
	big := MustNewRat(math.MaxInt64, 1)
	if _, err := big.Mul(MustNewRat(2, 1)); !errors.Is(err, errOverflow) {
		t.Fatalf("rat mul overflow not detected")
	}
}

func TestMediaErrorContext(t *testing.T) {
	e := AtError(CodeMfhdGap, PhaseValidate, 42, "missing seq %d", 5).
		WithContext("video_90000", "frag_0005.m4s").WithBox("mfhd")
	if e.Representation != "video_90000" || e.Fragment != "frag_0005.m4s" ||
		e.ByteOffset != 42 || e.Box != "mfhd" {
		t.Fatalf("context lost: %+v", e)
	}
	wrapped := errors.New("disk on fire")
	me := AsMedia(wrapped)
	if me.Code != CodeIO || !errors.Is(me, wrapped) {
		t.Fatalf("AsMedia wrapping failed: %+v", me)
	}
	if AsMedia(nil) != nil {
		t.Fatal("AsMedia(nil) must be nil")
	}
}
