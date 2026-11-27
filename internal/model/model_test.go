package model

import (
	"errors"
	"testing"
)

func TestNewRationalNormalizes(t *testing.T) {
	r, err := NewRational(6, -9)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != -2 || r.Den != 3 {
		t.Fatalf("got %+v", r)
	}
	if _, err := NewRational(1, 0); err == nil {
		t.Fatal("expected zero denominator error")
	}
}

func TestRationalArithmetic(t *testing.T) {
	// 1/30000 + 1/90000 == 1/22500 exactly, no float drift.
	a := MustRational(1, 30000)
	b := MustRational(1, 90000)
	sum := a.Add(b)
	if sum.Num != 1 || sum.Den != 22500 {
		t.Fatalf("sum = %v", sum)
	}
	if a.Cmp(b) <= 0 {
		t.Fatal("1/30000 must exceed 1/90000")
	}
	// Half-away rounding: 45000/90000 style midpoint.
	mid := MustRational(3, 2)
	if mid.Round() != 2 {
		t.Fatalf("round = %d", mid.Round())
	}
	neg := MustRational(-3, 2)
	if neg.Floor() != -2 || neg.Round() != -2 {
		t.Fatalf("neg floor/round = %d/%d", neg.Floor(), neg.Round())
	}
}

func TestScaleToMasterExact(t *testing.T) {
	// 1 tick at 30000 Hz = exactly 3 master ticks at 90000 Hz.
	exact, tick := ScaleToMaster(1, 30000)
	if exact.Num != 3 || exact.Den != 1 || tick != 3 {
		t.Fatalf("exact=%v tick=%d", exact, tick)
	}
	// 48000 -> 90000 scaling of one audio frame: 15/8, rounds to 2.
	exact, tick = ScaleToMaster(1, 48000)
	if exact.Num != 15 || exact.Den != 8 || tick != 2 {
		t.Fatalf("exact=%v tick=%d", exact, tick)
	}
	// Rational accumulation stays exact across any number of frames;
	// rounding happens once at the boundary, never per-step.
	sum := Rational{Num: 0, Den: 1}
	one := MustRational(15, 8) // 1 frame @48k expressed in master ticks
	for i := 0; i < 1000; i++ {
		sum = sum.Add(one)
	}
	if sum.Num != 1875 || sum.Den != 1 {
		t.Fatalf("exact accumulation = %v", sum)
	}
	if d := DeltaMaster(1000, 48000) - sum.Round(); d != 0 {
		t.Fatalf("final rounding mismatch %d", d)
	}
}

func TestExpandClockWrap(t *testing.T) {
	const mod = uint64(1 << 32)
	ext, wrapped := ExpandClock(mod+100, 50, mod)
	if !wrapped || ext != 2*mod+50 {
		t.Fatalf("wrap ext=%d wrapped=%v", ext, wrapped)
	}
	ext, wrapped = ExpandClock(100, 200, mod)
	if wrapped || ext != 200 {
		t.Fatalf("normal ext=%d wrapped=%v", ext, wrapped)
	}
	// Raw value is preserved by the caller contract: expansion only changes
	// the extended axis; RTPRaw stays available on the sample.
	if BitLen32(uint32(mod-1)) != 32 {
		t.Fatal("modulus bit length")
	}
}

func TestMediaErrorAnnotations(t *testing.T) {
	err := NewError(CodeTruncatedBox, "box cut short").
		WithFile("a.mp4").WithOffset(42).WithTrack(2).WithTick(99).WithEvent(7)
	if err.Error() == "" || err.Offset != 42 || err.EventSeq != 7 {
		t.Fatalf("annotation lost: %+v", err)
	}
	var me *MediaError
	if !errors.As(err, &me) || CodeOf(err) != CodeTruncatedBox {
		t.Fatal("errors.As / CodeOf failed")
	}
	if CodeOf(nil) != CodeOK {
		t.Fatal("nil must map to CodeOK")
	}
	if CodeOf(errors.New("plain")) != CodeMalformedContainer {
		t.Fatal("plain error fallback code")
	}
}
