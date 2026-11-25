package clock

import "testing"

func TestNewTimeBaseFallback(t *testing.T) {
	tb := NewTimeBase(0)
	if got := tb.FrameDurNS(); got != 40_000_000 {
		t.Fatalf("default frame duration = %d, want 40000000", got)
	}
	tb = NewTimeBase(-5)
	if got := tb.FrameDurNS(); got != 40_000_000 {
		t.Fatalf("negative frame duration fallback = %d", got)
	}
}

func TestFrameMapping(t *testing.T) {
	tb := NewTimeBase(40_000_000) // 25 fps
	if got := tb.FrameToNS(3); got != 120_000_000 {
		t.Fatalf("frame 3 start = %d", got)
	}
	if got := tb.FrameEndNS(3); got != 160_000_000 {
		t.Fatalf("frame 3 end = %d", got)
	}
	iv := tb.FrameInterval(2)
	if iv.StartNS != 80_000_000 || iv.EndNS != 120_000_000 || iv.DurationNS() != 40_000_000 {
		t.Fatalf("unexpected interval %+v", iv)
	}
	if !iv.Contains(100_000_000) || iv.Contains(120_000_000) {
		t.Fatal("half-open interval boundary check failed")
	}
}

func TestRTPRoundTrip(t *testing.T) {
	tb := NewTimeBase(40_000_000)
	// 40 ms at 90 kHz = 3600 ticks exactly.
	for frame := uint64(0); frame < 256; frame++ {
		ns := tb.FrameToNS(frame)
		ts := tb.NSToRTP(ns)
		if got := tb.RTPToNS(ts); got != ns {
			t.Fatalf("frame %d: round trip %d -> %d -> %d", frame, ns, ts, got)
		}
	}
	if got := tb.NSToRTP(40_000_000); got != 3600 {
		t.Fatalf("40ms RTP ticks = %d, want 3600", got)
	}
	// Negative offsets clamp to zero rather than wrapping.
	if got := tb.NSToRTP(-1); got != 0 {
		t.Fatalf("negative offset produced %d", got)
	}
}

func TestRTPWrap(t *testing.T) {
	tb := NewTimeBase(40_000_000)
	// One RTP wrap = 2^32 ticks ≈ 47721.8588 s on the 90 kHz clock.
	wrapNS := int64(47_721_858_844_444)
	// At the wrap boundary the uint32 value returns to zero.
	if got := tb.NSToRTP(wrapNS); got != 0 {
		t.Fatalf("wrap boundary timestamp = %d, want 0", got)
	}
}
