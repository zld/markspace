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

func TestDeclaredSwitchAcceptsBackwardTimestamp(t *testing.T) {
	n, _ := NewNormalizer(Config{BitWidth: 8})
	_ = n.Push(0, 100, 0)
	_ = n.Push(1, 110, 5)
	// New source restarts its RTP clock low. Without a switch declaration this
	// would be a boundary error; with one it is normalized.
	n.SwitchAt()
	if err := n.Push(2, 20, 9); err != nil {
		t.Fatalf("declared switch must accept backward ts: %v", err)
	}
	got := n.Samples()
	if !got[2].Switched {
		t.Fatal("sample 2 must be flagged as a source switch")
	}
	if got[2].NormalizedTick != 10 {
		t.Fatalf("normalized tick after switch = %d, want 10", got[2].NormalizedTick)
	}
	if got[2].OriginalRTP != 20 {
		t.Fatal("original RTP timestamp must be preserved after switch")
	}
	// Subsequent packets continue from the new raw origin, still monotonic.
	if err := n.Push(3, 35, 13); err != nil {
		t.Fatalf("post-switch push: %v", err)
	}
	if n.Samples()[3].NormalizedTick != 25 {
		t.Fatalf("tick after post-switch delta = %d, want 25", n.Samples()[3].NormalizedTick)
	}
}

func TestSwitchAtConsumedOnce(t *testing.T) {
	n, _ := NewNormalizer(Config{BitWidth: 8})
	_ = n.Push(0, 100, 0)
	n.SwitchAt()
	_ = n.Push(1, 110, 5) // forward, consumes the flag
	// The flag must not leak onto a later undeclared regression.
	if err := n.Push(2, 90, 9); perr.AsError(err) == nil {
		t.Fatal("an undeclared regression after a consumed switch must still fail")
	}
}
