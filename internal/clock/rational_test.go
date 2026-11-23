package clock

import (
	"errors"
	"math"
	"testing"
)

func TestNewReducesAndNormalizes(t *testing.T) {
	r := New(6, -9)
	if r.Num() != -2 || r.Den() != 3 {
		t.Fatalf("New(6,-9) = %s, want -2/3", r)
	}
	if New(0, 7).String() != "0/1" {
		t.Fatalf("zero not normalized: %s", New(0, 7))
	}
}

func TestNewZeroDenominatorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on zero denominator")
		}
	}()
	_ = New(1, 0)
}

func TestAddSubMulExact(t *testing.T) {
	a := New(1, 3)
	b := New(1, 6)
	s, err := a.Add(b)
	if err != nil || s.String() != "1/2" {
		t.Fatalf("1/3+1/6 = %s, %v", s, err)
	}
	d, err := a.Sub(b)
	if err != nil || d.String() != "1/6" {
		t.Fatalf("1/3-1/6 = %s, %v", d, err)
	}
	m, err := a.Mul(b)
	if err != nil || m.String() != "1/18" {
		t.Fatalf("1/3*1/6 = %s, %v", m, err)
	}
}

func TestOverflowReportedNotWrapped(t *testing.T) {
	big := New(math.MaxInt64, 1)
	if _, err := big.Add(New(1, 1)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("add overflow: %v", err)
	}
	if _, err := big.Mul(New(2, 1)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("mul overflow: %v", err)
	}
	if _, err := big.Sub(New(math.MinInt64, 1)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("sub overflow: %v", err)
	}
	// Cross multiplication MaxInt64*MaxInt64 cannot be represented.
	if _, err := New(math.MaxInt64, 2).Cmp(New(1, math.MaxInt64)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("cmp overflow: %v", err)
	}
}

func TestCmpOrdering(t *testing.T) {
	cases := []struct {
		a, b Rational
		want int
	}{
		{New(1, 3), New(1, 2), -1},
		{New(5, 4), New(10, 8), 0},
		{New(-1, 2), New(1, 2), -1},
		{New(7, 3), New(2, 1), 1},
	}
	for _, c := range cases {
		got, err := c.a.Cmp(c.b)
		if err != nil || got != c.want {
			t.Fatalf("Cmp(%s,%s)=%d,%v want %d", c.a, c.b, got, err, c.want)
		}
	}
}

func TestScaleAndToTicksRoundTrip(t *testing.T) {
	// 48000 ticks at 48kHz == 1 second == 90000 ticks at 90kHz.
	r, err := ScaleUnits(48000, 48000)
	if err != nil {
		t.Fatal(err)
	}
	ticks, err := r.ToTicks(90000)
	if err != nil || ticks != 90000 {
		t.Fatalf("round trip ticks = %d, %v", ticks, err)
	}
	// 1 tick at 90kHz truncates to 0 at 1Hz.
	one, _ := ScaleUnits(1, 90000)
	if v, _ := one.ToTicks(1); v != 0 {
		t.Fatalf("truncation = %d, want 0", v)
	}
	if _, err := ScaleUnits(1, 0); err == nil {
		t.Fatal("expected error on non-positive time base")
	}
}

func TestMillisecondsReportingOnly(t *testing.T) {
	r := New(1, 2)
	if r.Milliseconds() != 500.0 {
		t.Fatalf("half second in ms = %f", r.Milliseconds())
	}
}
