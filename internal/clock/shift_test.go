package clock

import (
	"errors"
	"testing"
)

func TestMakeShiftNormalizes(t *testing.T) {
	s, err := MakeShift(3, -9)
	if err != nil {
		t.Fatal(err)
	}
	if s.N != -1 || s.D != 3 {
		t.Fatalf("got %d/%d", s.N, s.D)
	}
	if _, err := MakeShift(1, 0); !errors.Is(err, ErrOverflow) {
		t.Fatalf("zero denominator: %v", err)
	}
}

func TestShiftApply(t *testing.T) {
	base := MustMake(5, 25) // 0.2s
	fwd, err := MakeShift(1, 25)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fwd.Apply(base)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmp(MustMake(6, 25)) != 0 {
		t.Fatalf("forward shift: %s", got)
	}
	back := fwd.Neg()
	got2, err := back.Apply(base)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Cmp(MustMake(4, 25)) != 0 {
		t.Fatalf("backward shift: %s", got2)
	}
	if _, err := back.Apply(Zero()); !errors.Is(err, ErrOverflow) {
		t.Fatalf("negative result: %v", err)
	}
}

func TestShiftBetween(t *testing.T) {
	a := MustMake(10, 25)
	b := MustMake(7, 25)
	s, err := ShiftBetween(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if s.Cmp(Shift{N: 3, D: 25}) != 0 {
		t.Fatalf("shift = %s", s)
	}
}

func TestShiftAddAndCmp(t *testing.T) {
	x, _ := MakeShift(1, 4)
	y, _ := MakeShift(1, 4)
	z, err := x.Add(y)
	if err != nil {
		t.Fatal(err)
	}
	if z.Cmp(Shift{N: 1, D: 2}) != 0 {
		t.Fatalf("add = %s", z)
	}
	if x.Cmp(y) != 0 || x.Neg().Cmp(x) >= 0 {
		t.Fatal("cmp wrong")
	}
}
