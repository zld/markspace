package source

import (
	"testing"

	"gamepack/internal/clock"
	perr "gamepack/internal/errors"
)

func TestMonotonicNormalization(t *testing.T) {
	n, err := NewNormalizer(Config{BitWidth: 32})
	if err != nil {
		t.Fatal(err)
	}
	seq := []uint32{1000, 4000, 7000}
	for i, ts := range seq {
		if err := n.Push(i, ts, int64(i*10)); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}
	got := n.Samples()
	want := []clock.Tick{0, 3000, 6000}
	for i, w := range want {
		if got[i].NormalizedTick != w {
			t.Fatalf("sample %d tick = %d, want %d", i, got[i].NormalizedTick, w)
		}
		if got[i].OriginalRTP != seq[i] || got[i].OriginalOffset != int64(i*10) {
			t.Fatalf("original coordinates not preserved on sample %d", i)
		}
	}
}

func TestWraparoundHalfPeriod(t *testing.T) {
	n, _ := NewNormalizer(Config{BitWidth: 8}) // period 256, half 128
	// Upper-half value 200 wraps to lower-half 20: delta = (256-200)+20 = 76.
	if err := n.Push(0, 200, 0); err != nil {
		t.Fatal(err)
	}
	if err := n.Push(1, 20, 5); err != nil {
		t.Fatalf("wrap should be accepted: %v", err)
	}
	got := n.Samples()
	if !got[1].Wrapped || got[1].NormalizedTick != 76 {
		t.Fatalf("wrap sample = %+v", got[1])
	}
	// Original RTP value is retained for traceability.
	if got[1].OriginalRTP != 20 {
		t.Fatal("original RTP timestamp must be preserved")
	}
}

func TestBackwardWithoutWrapIsBoundaryError(t *testing.T) {
	n, _ := NewNormalizer(Config{BitWidth: 8})
	// Both values in the lower half: a small regression is not a wrap.
	if err := n.Push(0, 100, 0); err != nil {
		t.Fatal(err)
	}
	err := n.Push(1, 90, 7)
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeClockBackward || pe.Offset != 7 {
		t.Fatalf("want CLOCK_BACKWARD at byte 7, got %v", err)
	}
	// The failed sample must not enter the output.
	if len(n.Samples()) != 1 {
		t.Fatal("failed push must not append a sample")
	}
}

func TestUpperHalfRegressionIsNotWrap(t *testing.T) {
	n, _ := NewNormalizer(Config{BitWidth: 8})
	// Both in the upper half: 200 -> 150 is a regression, not a wrap.
	_ = n.Push(0, 200, 0)
	if err := n.Push(1, 150, 9); perr.AsError(err) == nil {
		t.Fatal("upper-half regression must be a boundary error")
	}
}

func TestInvalidBitWidth(t *testing.T) {
	if _, err := NewNormalizer(Config{BitWidth: 0}); err == nil {
		t.Fatal("bit width 0 must fail")
	}
	if _, err := NewNormalizer(Config{BitWidth: 33}); err == nil {
		t.Fatal("bit width 33 must fail")
	}
}

func TestMarkSwitch(t *testing.T) {
	n, _ := NewNormalizer(Config{BitWidth: 32})
	_ = n.Push(0, 100, 0)
	_ = n.Push(1, 200, 5)
	n.MarkSwitch(1)
	if !n.Samples()[1].Switched || n.Samples()[0].Switched {
		t.Fatal("only ordinal 1 should be flagged as switched")
	}
}
