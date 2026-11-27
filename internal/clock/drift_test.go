package clock

import (
	"testing"

	"rtx/internal/model"
)

func samples(n int, stepNS int64, driftPPM float64) []Sample {
	out := make([]Sample, n)
	for i := range out {
		x := int64(i) * stepNS
		out[i] = Sample{Index: i, SampleNS: x, RTPNS: int64(float64(x) * (1 + driftPPM/1e6))}
	}
	return out
}

func TestFitZeroDrift(t *testing.T) {
	res := Fit(samples(50, 20_000_000, 0), 300)
	if res.PPM > 1 || res.PPM < -1 {
		t.Fatalf("want ~0 ppm, got %f", res.PPM)
	}
	if len(res.Exceeds) != 0 {
		t.Fatalf("no ranges expected, got %+v", res.Exceeds)
	}
}

func TestFitKnownDrift(t *testing.T) {
	res := Fit(samples(100, 20_000_000, 250), 300)
	if res.PPM < 240 || res.PPM > 260 {
		t.Fatalf("want ~250 ppm, got %f", res.PPM)
	}
	if len(res.Exceeds) != 0 {
		t.Fatalf("250 ppm is under the 300 ppm threshold, got %+v", res.Exceeds)
	}
}

func TestFitExceedingDriftMarksRange(t *testing.T) {
	s := samples(60, 20_000_000, 0)
	// Final 20 samples drift at 1000 ppm.
	for i := 40; i < 60; i++ {
		extra := float64(s[i].SampleNS-s[40].SampleNS) * 1000 / 1e6
		s[i].RTPNS += int64(extra)
	}
	res := Fit(s, 300)
	if len(res.Exceeds) == 0 {
		t.Fatal("drift-exceeding range not marked")
	}
	r := res.Exceeds[len(res.Exceeds)-1]
	if r.Start < 39 || r.End != 60 {
		t.Fatalf("range %v does not cover the drifting tail", r)
	}
	var anomaly bool
	for _, a := range res.Anomalies {
		if a.Reason == model.ReasonDrift {
			anomaly = true
		}
	}
	if !anomaly {
		t.Fatal("drift anomaly missing")
	}
	// Inputs must not be rewritten.
	if s[59].SampleNS != 59*20_000_000 {
		t.Fatal("input sample time modified")
	}
}

func TestFitResiduals(t *testing.T) {
	s := samples(30, 10_000_000, 100)
	s[15].RTPNS += 5_000_000 // 5 ms spike
	res := Fit(s, 300)
	if res.MaxResid < 4_000_000 {
		t.Fatalf("spike not visible in residuals: %f", res.MaxResid)
	}
	if len(res.Residual) != 30 {
		t.Fatalf("want 30 residuals, got %d", len(res.Residual))
	}
}
