package clock

import (
	"math"
	"testing"
)

// genFeed simulates a track whose device clock drifts by ppm from nominal.
// It returns observations whose nominal cadence is blockNs but whose raw
// millisecond counter advances at (1+ppm) of the nominal rate.
func genFeed(n int, startMs uint32, blockNs int64, ppm float64) []Observation {
	obs := make([]Observation, 0, n)
	for i := 0; i < n; i++ {
		trueNs := int64(i) * blockNs
		driftedMs := float64(trueNs) * (1 + ppm/1e6) / 1e6
		raw := uint32(int64(startMs) + int64(math.Round(driftedMs)))
		obs = append(obs, Observation{Track: 1, RawMs: raw, NominalNs: blockNs})
	}
	return obs
}

func TestUnwrapCrossesWrap(t *testing.T) {
	tl := New(1, Options{MinSamples: 1000}) // force rate fallback
	start := uint32(WrapPeriodMs - 5)
	var last int64 = math.MinInt64
	for i := 0; i < 12; i++ {
		raw := start + uint32(i)
		ns := tl.Observe(Observation{Track: 1, RawMs: raw, NominalNs: 40e6})
		if ns <= last {
			t.Fatalf("unwrapped time not increasing at step %d: %d <= %d", i, ns, last)
		}
		last = ns
	}
	// The observation 5 ms after wrap must be more than one wrap period.
	if got := last; got <= WrapPeriodMs*NanosPerMs {
		t.Fatalf("wrap not unwrapped: last=%d", got)
	}
	if err := tl.Calibrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok := tl.DriftPPM(1); !ok {
		t.Fatal("expected drift for track")
	}
}

func TestSmallBackwardsJitterStaysMonotonic(t *testing.T) {
	tl := New(1, Options{MinSamples: 1000})
	seq := []uint32{100, 140, 120, 180, 220}
	var prev int64
	for i, raw := range seq {
		ns := tl.Observe(Observation{Track: 1, RawMs: raw, NominalNs: 40e6})
		if i > 0 && ns < prev {
			t.Fatalf("step %d went backwards: %d < %d", i, ns, prev)
		}
		prev = ns
	}
}
