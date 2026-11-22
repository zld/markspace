package clock

import (
	"errors"
	"math"
	"testing"
)

func mustNew(t *testing.T, n, d int64) Rational {
	t.Helper()
	r, err := New(n, d)
	if err != nil {
		t.Fatalf("New(%d,%d): %v", n, d, err)
	}
	return r
}

func TestNewNormalizes(t *testing.T) {
	cases := []struct {
		n, d int64
		want string
	}{
		{6, 8, "3/4"},
		{-6, 8, "-3/4"},
		{6, -8, "-3/4"},
		{0, -5, "0"},
		{10, 5, "2"},
	}
	for _, c := range cases {
		if got := mustNew(t, c.n, c.d).String(); got != c.want {
			t.Errorf("New(%d,%d) = %s, want %s", c.n, c.d, got, c.want)
		}
	}
	if _, err := New(1, 0); !errors.Is(err, ErrZeroDenominator) {
		t.Fatalf("New(1,0) err = %v, want ErrZeroDenominator", err)
	}
}

func TestAddSubExactNoFloatDrift(t *testing.T) {
	// 1001/30000 seconds added 30000 times must be exactly 1001 seconds.
	one := mustNew(t, 1001, 30000)
	acc := Zero
	for i := 0; i < 30000; i++ {
		var err error
		acc, err = acc.Add(one)
		if err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if acc.String() != "1001" {
		t.Fatalf("accumulated %s, want 1001 (float drift)", acc)
	}
	back, err := acc.Sub(mustNew(t, 1001, 1))
	if err != nil {
		t.Fatal(err)
	}
	if !back.IsZero() {
		t.Fatalf("difference = %s, want 0", back)
	}
}

func TestCmpAcrossSignsAndScales(t *testing.T) {
	a := mustNew(t, 1, 3)
	b := mustNew(t, 2, 6)
	if a.Cmp(b) != 0 || !a.Equal(b) {
		t.Fatalf("%s vs %s should be equal", a, b)
	}
	if !mustNew(t, -1, 3).Less(Zero) {
		t.Fatal("-1/3 should be less than zero")
	}
	if !mustNew(t, 1, 1000000).Less(mustNew(t, 1, 999999)) {
		t.Fatal("1/1000000 should be less than 1/999999")
	}
}

func TestFloorTicks(t *testing.T) {
	cases := []struct {
		r     Rational
		scale uint32
		want  int64
	}{
		{mustNew(t, 1, 2), 90000, 45000},
		{mustNew(t, 1001, 30000), 1000, 33},
		{mustNew(t, -1, 2), 90000, -45000},
		{mustNew(t, -1, 3), 1000, -334}, // floor, not truncation
	}
	for _, c := range cases {
		got, err := c.r.FloorTicks(c.scale)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s @%d = %d, want %d", c.r, c.scale, got, c.want)
		}
	}
}

func TestOverflowIsReported(t *testing.T) {
	big := FromInt(math.MaxInt64)
	if _, err := big.Add(FromInt(1)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("Add overflow err = %v, want ErrOverflow", err)
	}
	if _, err := mustNew(t, math.MaxInt64, 1).MulInt(2); !errors.Is(err, ErrOverflow) {
		t.Fatalf("MulInt overflow err = %v, want ErrOverflow", err)
	}
	if _, err := mustNew(t, math.MinInt64, 1).Neg(); !errors.Is(err, ErrOverflow) {
		t.Fatalf("Neg overflow err = %v, want ErrOverflow", err)
	}
}

func TestFromTicksRejectsZeroTimescale(t *testing.T) {
	if _, err := FromTicks(1, 0); err == nil {
		t.Fatal("expected error for zero timescale")
	}
}
