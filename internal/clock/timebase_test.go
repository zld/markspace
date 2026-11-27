package clock

import "testing"

func TestFromFramesAndDuration(t *testing.T) {
	// 30 fps divides the 90 kHz grid: exactly 3000 ticks per frame.
	d, err := FrameDuration(30)
	if err != nil || d != 3000 {
		t.Fatalf("FrameDuration(30) = %d, %v", d, err)
	}
	tk, rem := FromFrames(5, 30)
	if tk != 15_000 || rem != 0 {
		t.Fatalf("FromFrames(5,30) = %d rem %d", tk, rem)
	}
	// 23.976-style non-integer rate must be rejected, not rounded.
	if _, err := FrameDuration(0); err == nil {
		t.Fatal("zero frame rate must error")
	}
	if _, err := FrameDuration(97); err == nil {
		t.Fatal("non-dividing frame rate must error")
	}
}

func TestTickArithmetic(t *testing.T) {
	var a Tick = 1000
	b := a.Add(250)
	if b != 1250 || !b.IsAfter(a) || a.IsBefore(b) == false {
		t.Fatalf("tick arithmetic broken: %d", b)
	}
	if b.Distance(a) != 250 {
		t.Fatalf("distance = %d, want 250", b.Distance(a))
	}
	// Negative deltas are allowed while rebuilding a normalized timeline.
	if a.Add(-400) != 600 {
		t.Fatal("negative delta must be supported")
	}
}

func TestWrapGeometry(t *testing.T) {
	// 32-bit RTP clock: half period 2^31, full period 2^32.
	if HalfPeriod(32) != Tick(1<<31) {
		t.Fatalf("half period = %d", HalfPeriod(32))
	}
	if Period(32) != int64(1)<<32 {
		t.Fatalf("period = %d", Period(32))
	}
	if HalfPeriod(0) != 0 || Period(64) != 0 {
		t.Fatal("invalid bit widths must yield zero, not overflow")
	}
}
