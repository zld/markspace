package report

import (
	"math"
	"testing"

	"mediapack/internal/clock"
)

func TestPixelMeanInRange(t *testing.T) {
	frame := make([]byte, 256)
	for i := range frame {
		frame[i] = byte(i)
	}
	mean := PixelMean(frame)
	if mean < 0 || mean > 255 || math.Abs(mean-127.5) > 0.01 {
		t.Fatalf("pixel_mean=%.3f", mean)
	}
}

func TestPeakDBFS(t *testing.T) {
	if got := PeakDBFS([]float64{0, 0}); got != -96 {
		t.Fatalf("silence floor=%v", got)
	}
	got := PeakDBFS([]float64{0.5, -0.25})
	if math.Abs(got+6.020) > 0.01 {
		t.Fatalf("peak dbfs=%.3f want about -6.02", got)
	}
}

func TestAlignmentAndHex(t *testing.T) {
	ms := AlignmentErrorMS(clock.Ticks(clock.TicksPerSecond / 1000))
	if math.Abs(ms-1) > 0.001 {
		t.Fatalf("ms=%v", ms)
	}
	if !AllHex64("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") {
		t.Fatal("valid hash rejected")
	}
	if AllHex64("ABC") {
		t.Fatal("short hash accepted")
	}
}
