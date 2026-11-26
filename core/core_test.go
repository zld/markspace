package core

import (
	"errors"
	"testing"
)

func TestRatNormalize(t *testing.T) {
	r := NewRat(2000, 1000)
	if r.Num != 2 || r.Den != 1 {
		t.Fatalf("got %v", r)
	}
	r = NewRat(3, -6)
	if r.Num != -1 || r.Den != 2 {
		t.Fatalf("sign normalize: got %v", r)
	}
}

func TestRatArithmetic(t *testing.T) {
	a, b := NewRat(1, 2), NewRat(1, 3)
	if got := a.Add(b); got != NewRat(5, 6) {
		t.Fatalf("add: %v", got)
	}
	if got := a.Sub(b); got != NewRat(1, 6) {
		t.Fatalf("sub: %v", got)
	}
	if got := a.Mul(b); got != NewRat(1, 6) {
		t.Fatalf("mul: %v", got)
	}
	if a.Cmp(b) <= 0 || b.Cmp(a) >= 0 || a.Cmp(a) != 0 {
		t.Fatal("cmp")
	}
	if !NewRat(0, 7).IsZero() {
		t.Fatal("zero")
	}
}

func TestRatCrossDenominator(t *testing.T) {
	// 2000ms on a 1/1000 base must equal 180000 on a 90kHz base.
	if NewRat(2000, 1000).Cmp(NewRat(180000, 90000)) != 0 {
		t.Fatal("cross-denominator equality")
	}
	if NewRat(2000, 1000).Millis() != 2000 {
		t.Fatal("millis")
	}
}

func TestParseDecimal(t *testing.T) {
	cases := map[string]Rat{
		"2":     NewRat(2, 1),
		"2.000": NewRat(2, 1),
		"6.006": NewRat(3003, 500),
		"0.5":   NewRat(1, 2),
	}
	for in, want := range cases {
		got, err := ParseDecimal(in)
		if err != nil || got != want {
			t.Fatalf("ParseDecimal(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseDecimal("abc"); err == nil {
		t.Fatal("expected error for junk")
	}
	if _, err := ParseDecimal(""); err == nil {
		t.Fatal("expected error for empty")
	}
}

func TestExitCodes(t *testing.T) {
	if ExitCode(nil) != ExitOK {
		t.Fatal("nil")
	}
	if ExitCode(ErrStop) != ExitCheckpoint {
		t.Fatal("stop")
	}
	if ExitCode(E(KindUsage, "flags", "bad", nil)) != ExitUsage {
		t.Fatal("usage")
	}
	if ExitCode(E(KindInput, "read", "missing", nil)) != ExitUsage {
		t.Fatal("input")
	}
	if ExitCode(E(KindInspect, "scan", "corrupt", nil)) != ExitMismatch {
		t.Fatal("inspect")
	}
	wrapped := errors.Join(errors.New("x"), ErrStop)
	if ExitCode(wrapped) != ExitCheckpoint {
		t.Fatal("wrapped stop")
	}
}

func TestErrorMessage(t *testing.T) {
	e := E(KindInspect, "scan", "bad nal", nil).At("a.mp4", 42)
	s := e.Error()
	for _, want := range []string{"inspect", "scan", "a.mp4", "42"} {
		if !contains(s, want) {
			t.Fatalf("message %q missing %q", s, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
