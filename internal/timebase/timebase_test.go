package timebase

import "testing"

func TestScaleAndNanoseconds(t *testing.T) {
	r, err := New(1, 24)
	if err != nil {
		t.Fatal(err)
	}
	if ticks, _ := r.Ticks(); ticks != 3750 {
		t.Fatalf("ticks = %d, want 3750", ticks)
	}
	if ns, _ := r.Nanoseconds(); ns != 41_666_667 {
		t.Fatalf("ns = %d, want 41666667", ns)
	}
	// Round trip via FromTicks preserves the canonical clock.
	back, err := FromTicks(3750, 24)
	if err != nil {
		t.Fatal(err)
	}
	if back.Num != 1 || back.Den != 24 {
		t.Fatalf("from ticks = %d/%d, want 1/24", back.Num, back.Den)
	}
}

func TestZeroDenominatorRejected(t *testing.T) {
	if _, err := New(1, 0); err == nil {
		t.Fatal("zero denominator accepted")
	}
}

func TestOverflowDetected(t *testing.T) {
	// A huge numerator must trip the int64 overflow guard rather than wrap.
	r := Rational{Num: 1<<63 - 1, Den: 1}
	if _, err := r.Nanoseconds(); err == nil {
		t.Fatal("overflow not detected")
	}
}

func TestRationalComparison(t *testing.T) {
	a := Rational{Num: 1, Den: 24}
	b := Rational{Num: 2, Den: 48}
	if a.Cmp(b) != 0 || !a.Equal(b) {
		t.Fatal("equivalent rationals compared unequal")
	}
	if (Rational{Num: 1, Den: 24}).Cmp(Rational{Num: 2, Den: 24}) >= 0 {
		t.Fatal("ordering wrong")
	}
}

func TestDurationTicks(t *testing.T) {
	d := Duration{Count: 12, Increment: Rational{Num: 1, Den: 24}}
	ticks, err := d.Ticks()
	if err != nil {
		t.Fatal(err)
	}
	if ticks != 45000 { // half a second at 90 kHz
		t.Fatalf("duration ticks = %d, want 45000", ticks)
	}
}
