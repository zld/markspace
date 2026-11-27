package clock

import "testing"

func TestTimelineAdvance(t *testing.T) {
	tl := NewTimeline(48000)
	if got := tl.Advance(960); got != 0 {
		t.Fatalf("first start = %d, want 0", got)
	}
	if got := tl.Advance(960); got != 960 {
		t.Fatalf("second start = %d, want 960", got)
	}
	if tl.Elapsed() != 1920 || tl.Position() != 1920 {
		t.Fatalf("elapsed = %d, want 1920", tl.Elapsed())
	}
}

func TestTimelineConversions(t *testing.T) {
	tl := NewTimeline(48000)
	// 48000 samples is exactly one second.
	if got := tl.Micros(48000); got != 1_000_000 {
		t.Fatalf("micros = %d, want 1000000", got)
	}
	if got := tl.Millis(48000); got != 1000 {
		t.Fatalf("millis = %d, want 1000", got)
	}
	// 24 samples at 48k = 500 us exactly.
	if got := tl.Micros(24); got != 500 {
		t.Fatalf("micros(24) = %d, want 500", got)
	}
}

func TestConvertRate(t *testing.T) {
	// One second at 48k maps to one second at 24k.
	if got := ConvertRate(48000, 48000, 24000); got != 24000 {
		t.Fatalf("ConvertRate = %d, want 24000", got)
	}
	// 960 samples at 48k = 20ms -> 960 samples at 48k stays 960.
	if got := ConvertRate(960, 48000, 48000); got != 960 {
		t.Fatalf("identity ConvertRate = %d, want 960", got)
	}
}

func TestNewTimelineZeroRatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on zero rate")
		}
	}()
	NewTimeline(0)
}
