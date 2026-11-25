package media

import (
	"math"
	"testing"
	"time"
)

func TestTicksConversionsRoundTrip(t *testing.T) {
	for _, rate := range []uint32{AudioClockRate, VideoClockRate} {
		ms := TicksToMS(int64(rate)/2, rate) // half a second
		if math.Abs(ms-500) > 1e-9 {
			t.Fatalf("rate %d: got %f ms, want 500", rate, ms)
		}
		ticks := MSToTicks(ms, rate)
		if ticks != int64(rate)/2 {
			t.Fatalf("rate %d: round trip got %d", rate, ticks)
		}
	}
}

func TestExtendedSeq(t *testing.T) {
	tb := TimeBase{Cycles: 2}
	if got := tb.ExtendedSeq(5); got != 2*65536+5 {
		t.Fatalf("extended seq = %d", got)
	}
}

func TestEstimateDrift(t *testing.T) {
	// Exactly 125 ppm fast at 8000 Hz: sender advances 8001 ticks per
	// receiver second. Integer ticks are constructed directly so sub-tick
	// float truncation cannot dominate this short synthetic series.
	rate := AudioClockRate
	ppm := 125.0
	var pts []DriftPoint
	for i := 1; i <= 10; i++ {
		arrival := time.Duration(i) * time.Second
		sender := int64(rate)*int64(i) + int64(i)
		pts = append(pts, DriftPoint{Arrival: arrival, RTPTicks: sender})
	}
	got := EstimateDrift(pts, rate)
	if math.Abs(got-ppm) > 0.01 {
		t.Fatalf("drift = %.4f ppm, want %.2f", got, ppm)
	}
	if EstimateDrift(pts[:1], rate) != 0 {
		t.Fatal("single point must report zero drift")
	}
}

func TestCorrectTicks(t *testing.T) {
	// 100 ppm fast: 1,000,100 ticks correspond to ~1,000,000 corrected ticks.
	got := CorrectTicks(1_000_100, 100)
	if math.Abs(got-1_000_000) > 1.0 {
		t.Fatalf("corrected ticks = %f", got)
	}
}

func TestSupportedClockRate(t *testing.T) {
	if !SupportedClockRate(AudioClockRate) || !SupportedClockRate(VideoClockRate) {
		t.Fatal("sample rates must be supported")
	}
	if SupportedClockRate(44100) {
		t.Fatal("44100 is outside the sample scope")
	}
}

func TestLocatableError(t *testing.T) {
	err := NewError(KindBadInput, "BAD_MAGIC", "rtp", 3, "bad header")
	if err.Error() != "rtp: BAD_MAGIC at record offset 3: bad header" {
		t.Fatalf("unexpected message %q", err.Error())
	}
	le, ok := AsLocatable(err)
	if !ok || le.Kind != KindBadInput {
		t.Fatal("AsLocatable failed")
	}
}
