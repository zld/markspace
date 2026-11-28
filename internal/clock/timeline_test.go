package clock

import (
	"testing"

	"mediakernel/internal/fault"
)

func TestTicksToSeconds(t *testing.T) {
	r, err := TicksToSeconds(90000, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != 1 || r.Den != 1 {
		t.Fatalf("got %d/%d want 1/1", r.Num, r.Den)
	}
	r, err = TicksToSeconds(1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != 1 || r.Den != 3 {
		t.Fatalf("got %d/%d want 1/3", r.Num, r.Den)
	}
	if _, err := TicksToSeconds(5, 0); !fault.Is(err, fault.ErrUnrepresentable) {
		t.Fatalf("zero timescale: %v", err)
	}
}

func TestMapTicksExactAndInexact(t *testing.T) {
	got, err := MapTicks(90000, 90000, 1000)
	if err != nil || got != 1000 {
		t.Fatalf("got %d,%v want 1000,nil", got, err)
	}
	if _, err := MapTicks(1, 3, 2); !fault.Is(err, fault.ErrUnrepresentable) {
		t.Fatalf("inexact mapping must fail: %v", err)
	}
	if _, err := MapTicks(1, 0, 2); !fault.Is(err, fault.ErrUnrepresentable) {
		t.Fatalf("zero scale must fail: %v", err)
	}
}

func TestRationalArithmetic(t *testing.T) {
	a, _ := TicksToSeconds(1, 2)
	b, _ := TicksToSeconds(1, 3)
	s, err := Add(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if s.Num != 5 || s.Den != 6 {
		t.Fatalf("1/2+1/3 = %d/%d", s.Num, s.Den)
	}
	d, _ := Sub(a, b)
	if d.Num != 1 || d.Den != 6 {
		t.Fatalf("1/2-1/3 = %d/%d", d.Num, d.Den)
	}
	if Cmp(a, b) <= 0 || Cmp(b, a) >= 0 || Cmp(a, a) != 0 {
		t.Fatal("Cmp wrong")
	}
	ticks, err := s.ToTicks(6)
	if err != nil || ticks != 5 {
		t.Fatalf("5/6 s at scale 6 = %d,%v", ticks, err)
	}
	if _, err := s.ToTicks(4); !fault.Is(err, fault.ErrUnrepresentable) {
		t.Fatalf("5/6 s at scale 4 must fail: %v", err)
	}
}
