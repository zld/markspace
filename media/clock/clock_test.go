package clock

import (
	"errors"
	"math"
	"testing"
)

func mustClock(t *testing.T, ts uint32, frame int64) *Clock {
	t.Helper()
	c, err := New(ts, frame)
	if err != nil {
		t.Fatalf("New(%d,%d): %v", ts, frame, err)
	}
	return c
}

func TestNewRejectsInvalid(t *testing.T) {
	if _, err := New(0, 1); !errors.Is(err, ErrZeroTimescale) {
		t.Fatalf("timescale 0: %v", err)
	}
	if _, err := New(90000, 0); !errors.Is(err, ErrZeroFrame) {
		t.Fatalf("frame 0: %v", err)
	}
	if _, err := New(90000, -1); !errors.Is(err, ErrZeroFrame) {
		t.Fatalf("frame -1: %v", err)
	}
}

func TestFromNSExactAndRemainder(t *testing.T) {
	c := mustClock(t, 90000, 3000)
	cases := []struct {
		ns   int64
		want Tick
	}{
		{0, Tick{0, 0, NanosPerSecond}},
		{1_000_000_000, Tick{90000, 0, NanosPerSecond}},
		{1, Tick{0, 90_000, NanosPerSecond}},
		{1_000, Tick{0, 90_000_000, NanosPerSecond}},
		{1_500_000_000, Tick{135000, 0, NanosPerSecond}},
		{1_000_000_007, Tick{90000, 630_000, NanosPerSecond}},
	}
	for _, tc := range cases {
		got, err := c.FromNS(tc.ns)
		if err != nil {
			t.Fatalf("FromNS(%d): %v", tc.ns, err)
		}
		if got != tc.want {
			t.Fatalf("FromNS(%d) = %+v, want %+v", tc.ns, got, tc.want)
		}
	}
}

func TestToNSRoundTripIsExact(t *testing.T) {
	for _, ts := range []uint32{90000, 60000, 48000, 44100, 1000} {
		c := mustClock(t, ts, int64(ts)/30)
		for ns := int64(0); ns < 3_000_000_000; ns += 7_654_321 {
			tick, err := c.FromNS(ns)
			if err != nil {
				t.Fatalf("ts=%d FromNS(%d): %v", ts, ns, err)
			}
			back, rem, err := c.ToNS(tick)
			if err != nil {
				t.Fatalf("ts=%d ToNS(%+v): %v", ts, tick, err)
			}
			if back != ns || rem != 0 {
				t.Fatalf("ts=%d roundtrip %d -> %+v -> %d (rem %d)", ts, ns, tick, back, rem)
			}
		}
	}
}

func TestNegativeNSKeepsSign(t *testing.T) {
	c := mustClock(t, 90000, 3000)
	tick, err := c.FromNS(-1_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Value != -90000 || tick.Rem != 0 || tick.RemDen != NanosPerSecond {
		t.Fatalf("tick = %+v", tick)
	}
	back, _, err := c.ToNS(tick)
	if err != nil {
		t.Fatal(err)
	}
	if back != -1_000_000_000 {
		t.Fatalf("back = %d", back)
	}
	// A sub-tick fraction stays non-negative on a negative reading.
	neg, err := c.FromNS(-1_000_000_007)
	if err != nil {
		t.Fatal(err)
	}
	if neg.Value != -90001 || neg.Rem != NanosPerSecond-630_000 {
		t.Fatalf("neg = %+v", neg)
	}
	back, _, err = c.ToNS(neg)
	if err != nil {
		t.Fatal(err)
	}
	if back != -1_000_000_007 {
		t.Fatalf("neg back = %d", back)
	}
}

func TestNewTickCanonicalises(t *testing.T) {
	got, err := NewTick(10, 1500, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != 11 || got.Rem != 500 || got.RemDen != 1000 {
		t.Fatalf("got %+v", got)
	}
	neg, err := NewTick(0, -5, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if neg.Value != -1 || neg.Rem != 995 {
		t.Fatalf("neg %+v", neg)
	}
}

func TestCompare(t *testing.T) {
	a := Tick{Value: 5, Rem: 1, RemDen: 2} // 5.5
	b := Tick{Value: 5, Rem: 2, RemDen: 3} // 5.666..
	if a.Compare(b) >= 0 {
		t.Fatal("5.5 should precede 5.666")
	}
	if a.Compare(a) != 0 {
		t.Fatal("self compare")
	}
	c := Tick{Value: 6, Rem: 0, RemDen: 1}
	if a.Compare(c) >= 0 {
		t.Fatal("5.5 should precede 6")
	}
}

func TestAlign(t *testing.T) {
	c := mustClock(t, 90000, 3000)
	if got := c.AlignDown(Tick{Value: 90001}); got.Value != 90000 {
		t.Fatalf("AlignDown = %+v", got)
	}
	if got := c.AlignUp(Tick{Value: 90001}); got.Value != 93000 {
		t.Fatalf("AlignUp = %+v", got)
	}
	if got := c.AlignUp(Tick{Value: 90000}); got.Value != 90000 {
		t.Fatalf("AlignUp exact = %+v", got)
	}
	if got := c.AlignDown(Tick{Value: -1}); got.Value != -1 {
		t.Fatalf("AlignDown negative must pass through, got %+v", got)
	}
}

func TestSkewAndFrame(t *testing.T) {
	c := mustClock(t, 90000, 3000)
	if c.ExceedsFrame(Tick{Value: 3000}, Tick{Value: 6000}) {
		t.Fatal("one frame must not exceed")
	}
	if !c.ExceedsFrame(Tick{Value: 0}, Tick{Value: 3001}) {
		t.Fatal("3001 ticks must exceed one frame")
	}
	if c.FrameNS() != 33_333_333 {
		t.Fatalf("FrameNS = %d", c.FrameNS())
	}
	fr, err := c.FrameRatio()
	if err != nil || fr.Num != 3000 || fr.Den != 90000 {
		t.Fatalf("FrameRatio = %+v %v", fr, err)
	}
}

func TestOverflow(t *testing.T) {
	wide := mustClock(t, math.MaxUint32, 3000)
	if _, err := wide.FromNS(math.MaxInt64); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected overflow, got %v", err)
	}
	c := mustClock(t, 90000, 3000)
	if _, _, err := c.ToNS(Tick{Value: math.MaxInt64, RemDen: NanosPerSecond}); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected overflow, got %v", err)
	}
}

func TestRationalMul(t *testing.T) {
	r, err := NewRational(3000, 90000)
	if err != nil {
		t.Fatal(err)
	}
	q, rem, err := r.Mul(90000)
	if err != nil || q != 3000 || rem != 0 {
		t.Fatalf("Mul = %d,%d,%v", q, rem, err)
	}
	n, err := NewRational(1, -2)
	if err != nil {
		t.Fatal(err)
	}
	if n.Num != -1 || n.Den != 2 {
		t.Fatalf("normalise = %+v", n)
	}
	if _, err := NewRational(1, 0); !errors.Is(err, ErrZeroDen) {
		t.Fatalf("zero den: %v", err)
	}
}
