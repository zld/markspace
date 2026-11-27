package clock

import (
	"math"
	"testing"
)

func TestMulScaleExact(t *testing.T) {
	cases := []struct {
		value, num, den, want int64
	}{
		{27_000_000, 1_000_000_000, PCRRate, 1_000_000_000},
		{90_000, 1_000_000_000, PTSRate, 1_000_000_000},
		{3003, 1001, 3003, 1001}, // 3003*1001/3003 = 1001
		{-100, 90, 90, -100},
		{1, 1, 3, 0}, // integer quotient, exact rational callers use Rational
	}
	for _, c := range cases {
		got, err := MulScale(c.value, c.num, c.den)
		if err != nil {
			t.Fatalf("MulScale(%d,%d,%d): %v", c.value, c.num, c.den, err)
		}
		if got != c.want {
			t.Errorf("MulScale(%d,%d,%d)=%d want %d", c.value, c.num, c.den, got, c.want)
		}
	}
}

func TestMulScaleOverflow(t *testing.T) {
	if _, err := MulScale(math.MaxInt64, math.MaxInt64, 1); err == nil {
		t.Fatal("expected overflow error")
	}
	if _, err := MulScale(1, 1, 0); err == nil {
		t.Fatal("expected zero-denominator error")
	}
}

func TestTicksConversionsRoundtrip(t *testing.T) {
	// Exact round-trip requires ticks that map to whole nanoseconds, i.e.
	// multiples of 27 at the 27 MHz rate.
	for _, ticks := range []int64{0, 27, 27_000_000, 27_000_000*3600 - 27} {
		ns, err := TicksToNanos(ticks, PCRRate)
		if err != nil {
			t.Fatalf("TicksToNanos(%d): %v", ticks, err)
		}
		back, err := NanosToTicks(ns, PCRRate)
		if err != nil {
			t.Fatalf("NanosToTicks(%d): %v", ns, err)
		}
		if back != ticks {
			t.Errorf("roundtrip %d -> %d ns -> %d", ticks, ns, back)
		}
	}
}

func TestTicksConversionsSubNanosTruncated(t *testing.T) {
	// One 27 MHz tick is ~37 ns; integer conversion truncates rather than
	// inventing precision.
	ns, err := TicksToNanos(1, PCRRate)
	if err != nil || ns != 37 {
		t.Errorf("TicksToNanos(1)=%d, %v", ns, err)
	}
}

func TestAbsDiffAndCheckedAdd(t *testing.T) {
	if d, _ := AbsDiff(10, 4); d != 6 {
		t.Errorf("AbsDiff=%d want 6", d)
	}
	if d, _ := AbsDiff(-10, 4); d != 14 {
		t.Errorf("AbsDiff=%d want 14", d)
	}
	if _, err := AbsDiff(math.MinInt64, 1); err == nil {
		t.Error("expected overflow from AbsDiff")
	}
	if _, err := CheckedAdd(math.MaxInt64, 1); err == nil {
		t.Error("expected overflow from CheckedAdd")
	}
	if r, err := CheckedAdd(math.MaxInt64-1, 1); err != nil || r != math.MaxInt64 {
		t.Errorf("CheckedAdd normal case = %d, %v", r, err)
	}
}

func TestRationalNanos(t *testing.T) {
	r, err := NewRational(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := r.Nanos()
	if err != nil || ns != 500_000_000 {
		t.Errorf("Nanos=%d err=%v", ns, err)
	}
	if _, err := NewRational(1, 0); err == nil {
		t.Fatal("expected error for zero denominator")
	}
}
