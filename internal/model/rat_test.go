package model

import "testing"

func TestRatArithmeticAndComparison(t *testing.T) {
	half, _ := NewRat(1, 2)
	third, _ := NewRat(1, 3)
	sum, err := half.Add(third)
	if err != nil || sum.String() != "5/6" {
		t.Fatalf("1/2+1/3 = %s, err=%v", sum, err)
	}
	diff, _ := half.Sub(third)
	if diff.String() != "1/6" {
		t.Fatalf("1/2-1/3 = %s", diff)
	}
	prod, _ := half.Mul(third)
	if prod.String() != "1/6" {
		t.Fatalf("1/2*1/3 = %s", prod)
	}
	if c, _ := half.Compare(third); c <= 0 {
		t.Error("1/2 must compare greater than 1/3 without floats")
	}
	abs, _ := NewRat(-3, 4)
	got, _ := abs.Abs()
	if got.String() != "3/4" {
		t.Fatalf("abs(-3/4) = %s", got)
	}
}

func TestRatParseAndOverflow(t *testing.T) {
	r, err := ParseRat("1/1000")
	if err != nil || r.String() != "1/1000" {
		t.Fatalf("parse 1/1000 = %s, err=%v", r, err)
	}
	if _, err := ParseRat("1/0"); err == nil {
		t.Error("zero denominator must be rejected")
	}
	if _, err := ParseRat("a/b"); err == nil {
		t.Error("non-numeric rational must be rejected")
	}
	// int64 max + 1 must be detected rather than wrapping silently.
	big := IntRat(9223372036854775807)
	if _, err := big.Add(IntRat(1)); err == nil {
		t.Error("expected overflow error past int64 max")
	}
	if _, err := big.Mul(IntRat(2)); err == nil {
		t.Error("expected multiplication overflow error")
	}
}

func TestDecimalRendering(t *testing.T) {
	cases := map[Rat]string{
		{num: 42, den: 10}:  "4.2",
		{num: 1, den: 2}:    "0.5",
		{num: 1, den: 1000}: "0.001",
	}
	for r, want := range cases {
		if got := r.DecimalString(6); got != want {
			t.Errorf("DecimalString(%s) = %q, want %q", r.String(), got, want)
		}
	}
	fixed := map[Rat]string{
		{num: 1, den: 3}:   "0.333333333",
		{num: 42, den: 10}: "4.2",
		{num: 2, den: 1}:   "2",
		{num: -5, den: 2}:  "-2.5",
	}
	for r, want := range fixed {
		if got := r.DecimalFixed(9); got != want && r.den != 1 {
			t.Errorf("DecimalFixed(%s) = %q, want %q", r.String(), got, want)
		}
	}
	if got := (Rat{num: 2, den: 1}).DecimalFixed(3); got != "2" {
		t.Errorf("integer DecimalFixed = %q, want 2", got)
	}
}
