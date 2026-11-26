package media

import "testing"

func TestRationalCmpExact(t *testing.T) {
	cases := []struct {
		a, b Rational
		want int
	}{
		{Rational{1, 3}, Rational{2, 6}, 0},
		{Rational{1, 3}, Rational{1, 2}, -1},
		{Rational{3, 2}, Rational{4, 3}, 1},
		// Large numerators that would lose precision in float64.
		{Rational{1 << 40, 1}, Rational{(1 << 40) + 1, 1}, -1},
	}
	for _, c := range cases {
		if got := c.a.Cmp(c.b); got != c.want {
			t.Errorf("Cmp(%v,%v)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestNewRationalRejectsBadDenominator(t *testing.T) {
	if _, err := NewRational(1, 0); err == nil {
		t.Fatal("expected error for zero denominator")
	}
	if _, err := NewRational(1, -2); err == nil {
		t.Fatal("expected error for negative denominator")
	}
}

func TestTimeBaseExactConversion(t *testing.T) {
	// 90 kHz clock: one tick is 11111... ns; conversion must truncate.
	tb, err := NewTimeBase(90000)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := tb.TicksToNs(1)
	if err != nil || ns != 11111 {
		t.Fatalf("1 tick = %d ns, err=%v", ns, err)
	}
	// 90000 ticks is exactly one second, no accumulated drift.
	sec, err := tb.TicksToNs(90000)
	if err != nil || sec != NanoPerSecond {
		t.Fatalf("90000 ticks = %d, err=%v", sec, err)
	}
	// Round trip lands back on the same tick count.
	ticks, err := tb.NsToTicks(NanoPerSecond)
	if err != nil || ticks != 90000 {
		t.Fatalf("1 s = %d ticks, err=%v", ticks, err)
	}
}

func TestTimeBaseRejectsZero(t *testing.T) {
	if _, err := NewTimeBase(0); err == nil {
		t.Fatal("zero timescale must be rejected")
	}
	if _, err := NewTimeBaseRational(Rational{0, 1}); err == nil {
		t.Fatal("zero rational rate must be rejected")
	}
}

func TestWrapDelta32(t *testing.T) {
	// Classic RTP wrap near the 32-bit boundary.
	if d := WrapDelta32(0xfffffff0, 0x0000000a); d != 26 {
		t.Fatalf("wrap delta = %d, want 26", d)
	}
	if d := WrapDelta32(100, 100); d != 0 {
		t.Fatalf("equal stamp delta = %d, want 0", d)
	}
	if d := WrapDelta32(5, 25); d != 20 {
		t.Fatalf("forward delta = %d, want 20", d)
	}
}

func TestIntervalHalfOpen(t *testing.T) {
	iv := Interval{StartNs: 10, EndNs: 20}
	if err := iv.Validate(); err != nil {
		t.Fatal(err)
	}
	if !iv.Contains(10) || iv.Contains(20) {
		t.Fatal("half-open boundary semantics violated")
	}
	if iv.Duration() != 10 {
		t.Fatalf("duration = %d", iv.Duration())
	}
	bad := []Interval{{0, 0}, {10, 9}, {-1, 5}}
	for _, b := range bad {
		if err := b.Validate(); err == nil {
			t.Fatalf("expected rejection of %+v", b)
		}
	}
}

func TestIntervalOverlaps(t *testing.T) {
	a := Interval{0, 10}
	if a.Overlaps(Interval{10, 20}) {
		t.Fatal("touching half-open intervals must not overlap")
	}
	if !a.Overlaps(Interval{5, 15}) {
		t.Fatal("partially overlapping intervals not detected")
	}
}

func TestSumDurationsOverflow(t *testing.T) {
	if _, err := SumDurations(1<<62, 1<<62); err == nil {
		t.Fatal("expected overflow error")
	}
	sum, err := SumDurations(1, 2, 3)
	if err != nil || sum != 6 {
		t.Fatalf("sum = %d err=%v", sum, err)
	}
}
