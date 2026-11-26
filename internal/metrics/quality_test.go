package metrics

import (
	"math"
	"testing"
)

func TestBitrateAndDelta(t *testing.T) {
	actual, err := ActualBitrate(192, 48, 48000) // 48 stereo 16-bit frames
	if err != nil {
		t.Fatal(err)
	}
	if actual != 1_536_000 {
		t.Fatalf("actual=%.1f want 1536000", actual)
	}
	delta, err := DeltaPercent(1_536_000, actual)
	if err != nil || math.Abs(delta) > 0.0001 {
		t.Fatalf("delta=%.6f err=%v", delta, err)
	}
}

func TestSNRIdenticalAndQuantized(t *testing.T) {
	src := make([]int32, 4096)
	for i := range src {
		src[i] = int32(0.9 * float64(math.MaxInt32) * math.Sin(2*math.Pi*float64(i)/97))
	}
	if got := SNR(src, src); got < 90 {
		t.Fatalf("identical SNR=%.1f", got)
	}
	q16, err := QuantizeLeftAligned(src, 16)
	if err != nil {
		t.Fatal(err)
	}
	if got := SNR(src, q16); got < 90 {
		t.Fatalf("16-bit SNR=%.1f below 90 dB", got)
	}
}

func TestAlignmentDetectsOneSampleShift(t *testing.T) {
	ref := []int32{0, 100, 200, 300, 400, 500, 600, 700}
	delayed := append([]int32{0}, ref...)
	a := AlignAndSNR(ref, delayed, 2)
	if a.ShiftSamples != 1 || a.ErrorSamples > 1 {
		t.Fatalf("alignment=%+v want one-sample delay", a)
	}
	exact := AlignAndSNR(ref, ref, 2)
	if exact.ErrorSamples != 0 || exact.SNRDB < 90 {
		t.Fatalf("exact alignment=%+v", exact)
	}
}
