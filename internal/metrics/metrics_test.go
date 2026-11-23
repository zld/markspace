package metrics

import (
	"math"
	"testing"
)

func TestPercentileNearestRank(t *testing.T) {
	samples := make([]float64, 100)
	for i := range samples {
		samples[i] = float64(i + 1) // 1..100
	}
	if got := P95(samples); got != 95 {
		t.Fatalf("p95 = %f, want 95", got)
	}
	if got := Percentile(samples, 50); got != 50 {
		t.Fatalf("p50 = %f, want 50", got)
	}
	// Input must not be mutated.
	if samples[0] != 1 || samples[99] != 100 {
		t.Fatal("percentile must not reorder the caller's slice")
	}
}

func TestPercentileSmallAndEmpty(t *testing.T) {
	if got := P95(nil); got != 0 {
		t.Fatalf("empty p95 must be 0, got %f", got)
	}
	if got := P95([]float64{7}); got != 7 {
		t.Fatalf("single-value p95 = %f, want 7", got)
	}
}

func TestRecoveryRate(t *testing.T) {
	if RecoveryRate(1, 1) != 1.0 {
		t.Fatal("single recovered of one lost must be 1.0")
	}
	if RecoveryRate(0, 2) != 0.0 {
		t.Fatal("none recovered of two lost must be 0.0")
	}
	if RecoveryRate(5, 0) != 0.0 {
		t.Fatal("zero loss must report 0.0, not NaN")
	}
	if math.IsNaN(RecoveryRate(0, 0)) {
		t.Fatal("recovery rate must never be NaN")
	}
}

func TestMaxAbs(t *testing.T) {
	if MaxAbs([]float64{1, -9, 4}) != 9 {
		t.Fatal("max absolute value wrong")
	}
	if MaxAbs(nil) != 0 {
		t.Fatal("empty max must be zero")
	}
}

func TestSummaryJSONShape(t *testing.T) {
	s := Summary{SampleCount: 1000, P95LatencyMs: P95(linearSamples(1000))}
	if s.P95LatencyMs <= 0 || math.IsNaN(s.P95LatencyMs) {
		t.Fatalf("p95 must be finite positive, got %f", s.P95LatencyMs)
	}
}

func linearSamples(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = float64(i + 1)
	}
	return out
}
