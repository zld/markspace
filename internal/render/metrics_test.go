package render

import (
	"math"
	"testing"
)

func TestComputeRGBMean(t *testing.T) {
	// Two pixels: (0,10,20) and (255,20,40).
	rgba := []byte{0, 10, 20, 255, 255, 20, 40, 255}
	got, err := ComputeRGBMean(rgba)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.R-127.5) > 1e-9 || math.Abs(got.G-15) > 1e-9 || math.Abs(got.B-30) > 1e-9 {
		t.Fatalf("bad means %+v", got)
	}
	if _, err := ComputeRGBMean([]byte{1, 2, 3}); err == nil {
		t.Fatal("odd RGBA buffer must fail")
	}
}

func TestPeakDBFS(t *testing.T) {
	peak := PeakDBFS([]int16{0, 8192, -8191})
	if math.Abs(peak+12.04) > 0.1 {
		t.Fatalf("peak %.3f outside -12.04±0.1", peak)
	}
	if !math.IsInf(PeakDBFS(nil), -1) {
		t.Fatal("silence must be -Inf")
	}
}

func TestHashBytesLength(t *testing.T) {
	h := HashBytes([]byte("assetpack"))
	if len(h) != 64 {
		t.Fatalf("hash length %d", len(h))
	}
}
