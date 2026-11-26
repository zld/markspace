package media

import "testing"

func TestRationalConversionsAndExpectedTime(t *testing.T) {
	tb := MustRational(1, 48000)
	if got := tb.Microseconds(2000); got != 41667 {
		t.Fatalf("2000 ticks = %d us, want rounded 41667", got)
	}
	if got := tb.TicksFromMicroseconds(41667); got != 2000 {
		t.Fatalf("41667 us = %d ticks, want 2000", got)
	}

	fps := MustRational(24, 1)
	for i, wantTicks := range []int64{0, 2000, 4000, 6000} {
		if got := ExpectedTicks(i, fps, tb); got != wantTicks {
			t.Fatalf("frame %d ticks=%d want %d", i, got, wantTicks)
		}
	}
	if got := ExpectedTicks(11, fps, tb); got != 22000 {
		t.Fatalf("frame 11 ticks=%d want 22000", got)
	}
	if got := ExpectedMicroseconds(1, fps); got != 41667 {
		t.Fatalf("frame duration us=%d want 41667", got)
	}
}

func TestRationalRejectsZeroDenominator(t *testing.T) {
	if _, err := NewRational(1, 0); err == nil {
		t.Fatal("expected zero denominator error")
	}
}

func TestErrorContainsLocation(t *testing.T) {
	err := E(CodeChecksum, "manifest.validate", nil).
		WithFile("frames/f0001.png").WithFrame(7).WithOffset(128)
	me := AsError(err)
	if me.Code != CodeChecksum || me.Frame != 7 || me.Offset != 128 {
		t.Fatalf("unexpected error: %#v", me)
	}
	if !HasCode(err, CodeChecksum) {
		t.Fatal("HasCode failed")
	}
}
