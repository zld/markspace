package clock

import "testing"

func TestConvertMapping(t *testing.T) {
	tl := NewTimeline(DefaultTickRateHz)

	// 960 samples at 48 kHz -> 960 ticks (typical 20 ms Opus frame).
	if got := tl.Convert(960, DefaultTickRateHz); got != 960 {
		t.Fatalf("audio convert = %d, want 960", got)
	}
	// 1800 RTP video units at 90 kHz -> 960 ticks at 48 kHz.
	if got := tl.Convert(1800, VideoClockRateHz); got != 960 {
		t.Fatalf("video convert = %d, want 960", got)
	}
	// Deterministic floor mapping: 1 unit at 44.1 kHz -> 1 tick.
	if got := tl.Convert(1, 44100); got != 1 {
		t.Fatalf("floor convert = %d, want 1", got)
	}
	if got := tl.Convert(44100, 44100); got != DefaultTickRateHz {
		t.Fatalf("one second = %d, want %d", got, DefaultTickRateHz)
	}
}

func TestCommitAndCursor(t *testing.T) {
	tl := NewTimeline(DefaultTickRateHz)

	iv, err := tl.Commit(KindAudio, 0, 960)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if iv.End != 960 || tl.Cursor(KindAudio) != 960 {
		t.Fatalf("cursor = %d, want 960", tl.Cursor(KindAudio))
	}

	if _, err := tl.Commit(KindAudio, 960, 0); err != ErrEmpty {
		t.Fatalf("empty commit err = %v, want ErrEmpty", err)
	}
	// Rewriting a committed interval must be rejected (retry safety).
	if _, err := tl.Commit(KindAudio, 10, 100); err != ErrOverlap {
		t.Fatalf("overlap commit err = %v, want ErrOverlap", err)
	}
	// Audio and video cursors are independent.
	if tl.Cursor(KindVideo) != 0 {
		t.Fatalf("video cursor = %d, want 0", tl.Cursor(KindVideo))
	}
	if _, err := tl.Commit(KindVideo, 0, 480); err != nil {
		t.Fatalf("video commit: %v", err)
	}
	if len(tl.Intervals(KindAudio)) != 1 || len(tl.Intervals(KindVideo)) != 1 {
		t.Fatal("interval records missing")
	}
}

func TestMsRoundTrip(t *testing.T) {
	tl := NewTimeline(DefaultTickRateHz)
	ticks := tl.MsToTicks(1000)
	if ticks != DefaultTickRateHz || tl.TicksToMs(ticks) != 1000 {
		t.Fatalf("ms round trip broken: %d ticks -> %d ms", ticks, tl.TicksToMs(ticks))
	}
}

func TestZeroRateFallback(t *testing.T) {
	tl := NewTimeline(0)
	if tl.TickRate() != DefaultTickRateHz {
		t.Fatalf("fallback rate = %d", tl.TickRate())
	}
}
