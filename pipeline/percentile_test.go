package pipeline

import (
	"math"
	"testing"
)

func TestPercentileMsNearestRank(t *testing.T) {
	samples := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := []struct {
		p    float64
		want float64
	}{
		{50, 6}, {95, 10}, {0, 1}, {100, 10},
	}
	for _, c := range cases {
		if got := percentileMs(samples, c.p); got != c.want {
			t.Fatalf("p%v = %v want %v", c.p, got, c.want)
		}
	}
	// Ordering must not change the result.
	shuffled := []float64{10, 2, 8, 4, 6, 1, 9, 3, 7, 5}
	if percentileMs(shuffled, 95) != 10 {
		t.Fatal("percentile must sort before ranking")
	}
}

func TestPercentileMsEmptyAndSubMillisecond(t *testing.T) {
	if got := percentileMs(nil, 95); got != 0 {
		t.Fatalf("empty sample must be 0, got %v", got)
	}
	// Sub-millisecond microsecond-scale latencies stay visible as fractions.
	sub := []float64{0.012, 0.018, 0.021, 0.03}
	got := percentileMs(sub, 95)
	if math.Abs(got-0.03) > 1e-9 {
		t.Fatalf("sub-ms p95 = %v want 0.03", got)
	}
}
