package clock

import (
	"math"
	"testing"
)

func TestNewTimeBaseRejectsZero(t *testing.T) {
	if _, err := NewTimeBase(0); err == nil {
		t.Fatal("zero timescale must be rejected")
	}
}

func TestToNanosExactAndRemainder(t *testing.T) {
	// 90 kHz MPEG-2 video clock: 3003 ticks == 33,366,666.6... ns
	tb, err := NewTimeBase(90000)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tb.ToNanos(3003)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ns != 33_366_666 || got.Rem.Num != 60_000 || got.Rem.Den != 90000 {
		t.Fatalf("unexpected conversion %+v", got)
	}
	// Whole seconds must convert exactly with zero remainder.
	sec, err := tb.ToNanos(90000)
	if err != nil {
		t.Fatal(err)
	}
	if sec.Ns != NanosPerSecond || sec.Rem.Num != 0 {
		t.Fatalf("one second conversion wrong: %+v", sec)
	}
}

func TestToNanosNegativeKeepsNonNegativeRemainder(t *testing.T) {
	tb, _ := NewTimeBase(2)
	got, err := tb.ToNanos(-1) // -500ms
	if err != nil {
		t.Fatal(err)
	}
	if got.Ns != -500_000_000 || got.Rem.Num != 0 {
		t.Fatalf("negative tick conversion wrong: %+v", got)
	}
	tb3, _ := NewTimeBase(3)
	// -1e9/3 = -333333334 remainder 2 (Euclidean: q*3 + 2 = -1e9).
	g3, _ := tb3.ToNanos(-1)
	if g3.Ns != -333_333_334 || g3.Rem.Num != 2 || g3.Rem.Den != 3 {
		t.Fatalf("negative floor division wrong: %+v", g3)
	}
}

func TestToNanosOverflow(t *testing.T) {
	tb, _ := NewTimeBase(1)
	if _, err := tb.ToNanos(math.MaxInt64); err == nil {
		t.Fatal("max ticks must overflow")
	}
}

func TestUnwrapper32BitWraps(t *testing.T) {
	u, err := NewUnwrapper(32)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		raw     uint64
		want    int64
		wrapped bool
	}{
		{0xFFFFFFFE, 0xFFFFFFFE, false},
		{0xFFFFFFFF, 0xFFFFFFFF, false},
		{0, 1 << 32, true},
		{2, 1<<32 + 2, false},
		{1, 1<<32 + 1, false}, // small backwards step, not a wrap
	}
	for i, c := range cases {
		got, wrapped, err := u.Expand(c.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want || wrapped != c.wrapped {
			t.Fatalf("case %d: got %d/wrap=%v want %d/wrap=%v",
				i, got, wrapped, c.want, c.wrapped)
		}
	}
	if u.Wraps() != 1 {
		t.Fatalf("wrap count = %d, want 1", u.Wraps())
	}
}

func TestUnwrapper16BitSequence(t *testing.T) {
	u, _ := NewUnwrapper(16)
	prev := int64(-1)
	for i := 0; i < 200000; i++ {
		raw := uint64(i % 65536)
		got, _, err := u.Expand(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got != prev+1 { // every step must advance exactly one
			t.Fatalf("iteration %d: got %d, expected %d", i, got, prev+1)
		}
		prev = got
	}
	if u.Wraps() != 3 { // 200000/65536 -> 3 full cycles
		t.Fatalf("wraps = %d, want 3", u.Wraps())
	}
}

func TestFitDriftAndCorrect(t *testing.T) {
	// Observed clock runs 100ppm slow with a 500us offset.
	var anchors []Anchor
	for k := int64(0); k < 8; k++ {
		trueNS := k * 1_000_000_000
		obsNS := int64(float64(trueNS-500_000) / 1.0001)
		anchors = append(anchors, Anchor{ObsNS: obsNS, TrueNS: trueNS})
	}
	m, err := FitDrift(anchors)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(m.Slope-1.0001) > 1e-9 {
		t.Fatalf("slope = %v, want ~1.0001", m.Slope)
	}
	c, err := m.Correct(anchors[3].ObsNS)
	if err != nil {
		t.Fatal(err)
	}
	if abs(c-anchors[3].TrueNS) > 2_000 { // within 2us after rounding
		t.Fatalf("corrected %d vs true %d", c, anchors[3].TrueNS)
	}
}

func TestFitDriftRejectsDegenerate(t *testing.T) {
	if _, err := FitDrift(nil); err == nil {
		t.Fatal("no anchors must fail")
	}
	if _, err := FitDrift([]Anchor{{1, 1}}); err == nil {
		t.Fatal("single anchor must fail")
	}
	same := []Anchor{{10, 100}, {10, 200}, {10, 300}}
	if _, err := FitDrift(same); err == nil {
		t.Fatal("constant-x anchors must fail")
	}
}

func TestEnforceMonotonicVFR(t *testing.T) {
	in := []int64{0, 33_000_000, 33_000_000, 70_000_000, 50_000_000, 90_000_000}
	out, dups, disc := EnforceMonotonic(in)
	if len(dups) != 1 || dups[0] != 2 || out[2] != 33_000_001 {
		t.Fatalf("duplicate handling wrong: out=%v dups=%v", out, dups)
	}
	if len(disc) != 1 || disc[0] != 4 {
		t.Fatalf("discontinuity detection wrong: %v", disc)
	}
	wantSpaces := []int64{33_000_000, 1, 36_999_999, -20_000_000, 40_000_000}
	for i := 1; i < len(out); i++ {
		if out[i]-out[i-1] != wantSpaces[i-1] {
			t.Fatalf("VFR spacing altered at %d: %v", i, out)
		}
	}
}

func TestRatNanosCompare(t *testing.T) {
	a := RatNanos{Ns: 10, Rem: Fraction{1, 2}}
	b := RatNanos{Ns: 10, Rem: Fraction{3, 4}}
	if a.Compare(b) != -1 || b.Compare(a) != 1 {
		t.Fatal("fraction comparison failed")
	}
	c := RatNanos{Ns: 10, Rem: Fraction{2, 4}}
	if a.Compare(c) != 0 {
		t.Fatal("equivalent fractions must compare equal")
	}
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
