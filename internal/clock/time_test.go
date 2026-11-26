package clock

import (
	"errors"
	"math"
	"testing"
)

func TestRatBasic(t *testing.T) {
	a := MustR(1, 3)
	b := MustR(1, 6)
	sum, err := a.Add(b)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := sum.Cmp(MustR(1, 2)); c != 0 {
		t.Fatalf("1/3+1/6 = %s, want 1/2", sum)
	}
	diff, err := a.Sub(b)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := diff.Cmp(MustR(1, 6)); c != 0 {
		t.Fatalf("1/3-1/6 = %s, want 1/6", diff)
	}
	prod, err := a.Mul(MustR(3, 2))
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := prod.Cmp(MustR(1, 2)); c != 0 {
		t.Fatalf("1/3*3/2 = %s, want 1/2", prod)
	}
	quo, err := MustR(1, 2).Div(MustR(3, 4))
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := quo.Cmp(MustR(2, 3)); c != 0 {
		t.Fatalf("(1/2)/(3/4) = %s, want 2/3", quo)
	}
}

func TestNormalization(t *testing.T) {
	r, err := R(4, -8)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != -1 || r.Den != 2 {
		t.Fatalf("R(4,-8) = %s, want -1/2", r)
	}
	if _, err := R(1, 0); !errors.Is(err, ErrZeroDenominator) {
		t.Fatalf("zero denominator: %v", err)
	}
}

func TestFloorRound(t *testing.T) {
	cases := []struct {
		in        Rat
		wantFloor int64
		wantRound int64
	}{
		{MustR(5, 2), 2, 3},
		{MustR(-5, 2), -3, -3},
		{MustR(4, 2), 2, 2},
		{MustR(-4, 2), -2, -2},
		{MustR(1, 3), 0, 0},
		{MustR(-1, 3), -1, 0},
	}
	for _, tc := range cases {
		f, err := tc.in.Floor()
		if err != nil || f != tc.wantFloor {
			t.Errorf("%s floor = %d, %v; want %d", tc.in, f, err, tc.wantFloor)
		}
		rd, err := tc.in.Round()
		if err != nil || rd != tc.wantRound {
			t.Errorf("%s round = %d, %v; want %d", tc.in, rd, err, tc.wantRound)
		}
	}
}

func TestTicksAndNanoseconds(t *testing.T) {
	// 90000 ticks at 90 kHz == 1 s == 1e9 ns.
	r, err := TicksToRat(90000, 90000)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := r.ToNanoseconds()
	if err != nil || ns != 1_000_000_000 {
		t.Fatalf("ns = %d, err=%v", ns, err)
	}
	// 48 kHz RTP: one tick = 1/48000 s, round-trip through ns.
	r2, err := TicksToRat(48, 48000)
	if err != nil {
		t.Fatal(err)
	}
	ns2, err := r2.ToNanoseconds()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Nanoseconds(ns2)
	if err != nil || !back.Valid() {
		t.Fatalf("round trip failed: %s err=%v", back, err)
	}
	// 1 ms expressed at 48k ticks is 48 ticks exactly.
	oneMs, err := Nanoseconds(1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	ticks, err := oneMs.Mul(MustR(48000, 1))
	if err != nil {
		t.Fatal(err)
	}
	n, err := ticks.Round()
	if err != nil || n != 48 {
		t.Fatalf("1ms in 48k ticks = %d, err=%v", n, err)
	}
	_ = r2
}

func TestOverflow(t *testing.T) {
	big := MustR(math.MaxInt64, 1)
	if _, err := big.Mul(MustR(2, 1)); !IsOverflow(err) {
		t.Fatalf("max*2 should overflow, got %v", err)
	}
	if _, err := big.Add(MustR(1, 1)); !IsOverflow(err) {
		t.Fatalf("max+1 should overflow, got %v", err)
	}
	// Cross-multiply comparison overflow.
	if _, err := big.Cmp(MustR(1, math.MaxInt64)); !IsOverflow(err) {
		t.Fatalf("cmp overflow not detected, got %v", err)
	}
}

func TestMinMaxAndString(t *testing.T) {
	a, b := MustR(1, 3), MustR(1, 2)
	lo, err := Min(a, b)
	if err != nil || lo.Num != 1 || lo.Den != 3 {
		t.Fatalf("min = %s, %v", lo, err)
	}
	hi, err := Max(a, b)
	if err != nil || hi.Num != 1 || hi.Den != 2 {
		t.Fatalf("max = %s, %v", hi, err)
	}
	if got := MustR(7, 1).String(); got != "7" {
		t.Fatalf("string = %q", got)
	}
	if got := MustR(7, 3).String(); got != "7/3" {
		t.Fatalf("string = %q", got)
	}
}

func TestMediaErrorStructure(t *testing.T) {
	err := AtLine(AtFile(New(KindInvalidInput, "parse", "bad token"), "a.mpd"), 12)
	me := AsMedia(err)
	if me == nil || me.Kind != KindInvalidInput || me.File != "a.mpd" || me.Line != 12 {
		t.Fatalf("structured location lost: %+v", me)
	}
	if KindOf(err) != KindInvalidInput {
		t.Fatalf("kind = %s", KindOf(err))
	}
}
