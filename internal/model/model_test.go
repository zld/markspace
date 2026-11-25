package model

import (
	"errors"
	"testing"
)

func TestRationalRescaleRemainder(t *testing.T) {
	// 3000 ticks at 90kHz is 33.333...ms; the microsecond remainder must be
	// visible, not rounded away.
	ticks, rem, err := RescaleTicks(3000, 90000, 1000000)
	if err != nil {
		t.Fatal(err)
	}
	if ticks != 33333 || rem != 30000 {
		t.Fatalf("got ticks=%d remainder=%d", ticks, rem)
	}
	exact, rem, err := RescaleTicks(3_000_000, 1_000_000, 1_000_000)
	if err != nil || exact != 3_000_000 || rem != 0 {
		t.Fatalf("exact conversion got %d,%d,%v", exact, rem, err)
	}
}

func TestRationalNoFloatAccumulation(t *testing.T) {
	a := MustRational(1, 3)
	b := MustRational(1, 6)
	if a.Cmp(b) <= 0 {
		t.Fatal("1/3 must exceed 1/6")
	}
	if _, err := b.Sub(a); !errors.Is(err, ErrNegativeTime) {
		t.Fatalf("expected negative time, got %v", err)
	}
}

func TestUnwrap(t *testing.T) {
	var u UnwrapState
	u.Init(0xfffffff0)
	if err := push(t, &u, 0x00000010); err != nil {
		t.Fatal(err)
	}
	if u.Wraps() != 1 {
		t.Fatalf("wraps=%d", u.Wraps())
	}
	r, err := u.Push(0x00000020)
	if err != nil {
		t.Fatal(err)
	}
	if r.Value != (1<<32)|0x20 || r.Wraps != 1 || r.Wrapped {
		t.Fatalf("bad unwrap %+v", r)
	}
	if _, err := u.Push(0xffff0000); !errors.Is(err, ErrBackwards) {
		t.Fatalf("expected backwards, got %v", err)
	}
}

func push(t *testing.T, u *UnwrapState, ts uint32) error {
	t.Helper()
	_, err := u.Push(ts)
	return err
}

func TestStages(t *testing.T) {
	if len(StageOrder) != 6 {
		t.Fatalf("want 6 stages, got %d", len(StageOrder))
	}
	if StageIngest.Next() != StageNormalize || StageComplete.Next() != StageComplete {
		t.Fatal("bad stage order")
	}
}
