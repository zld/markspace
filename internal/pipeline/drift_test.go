package pipeline

import (
	"math"
	"testing"
)

func TestEstimateDriftFromObservations(t *testing.T) {
	// 1000-unit tick rate; source runs 120 ppm fast so 1000 ticks cover
	// only ~0.99988 reference seconds.
	p := New(120)
	obs := make([]DriftObservation, 0, 5)
	for i := 0; i <= 4; i++ {
		ref := float64(i) * 999_880.0 / 1_000_000.0
		obs = append(obs, DriftObservation{RefSeconds: ref, SourceTick: int64(i * 1000)})
	}
	fit := p.EstimateDrift(1000, obs)
	if fit.Observations != 5 {
		t.Fatalf("observation count = %d", fit.Observations)
	}
	if math.Abs(fit.EstimatedPPM-120) > 1.0 || !fit.Trusted {
		t.Fatalf("estimated ppm = %f trusted=%v, want ~120 trusted",
			fit.EstimatedPPM, fit.Trusted)
	}
	if fit.RSquared < 0.9999 || fit.AppliedPPM != 120 || len(fit.ResidualMicros) != 5 {
		t.Fatalf("fit record wrong: %+v", fit)
	}
}

func TestEstimateDriftRejectsPoorFit(t *testing.T) {
	// A discontinuity (non-linear jump) must fail the quality gate rather
	// than produce a misleading PPM.
	p := New(0)
	obs := []DriftObservation{
		{RefSeconds: 0, SourceTick: 0},
		{RefSeconds: 1, SourceTick: 1000},
		{RefSeconds: 2, SourceTick: 9000}, // out-of-line jump
		{RefSeconds: 3, SourceTick: 3000},
	}
	fit := p.EstimateDrift(1000, obs)
	if fit.Trusted {
		t.Fatalf("non-linear observations must not be trusted: %+v", fit)
	}
}

func TestEstimateDriftNeedsTwoPoints(t *testing.T) {
	p := New(0)
	fit := p.EstimateDrift(1000, []DriftObservation{{RefSeconds: 0, SourceTick: 0}})
	if fit.EstimatedPPM != 0 || fit.ResidualMicros != nil {
		t.Fatalf("single point must not fit: %+v", fit)
	}
}

func TestBuildObservationsUsesRecoverableHeaders(t *testing.T) {
	inputs := makeSegments(t, 4, false)
	for i := range inputs {
		inputs[i].DurationSeconds = 0.4
	}
	var parsed []*ParsedSegment
	for _, in := range inputs {
		ps := New(0).ParseStage(in)
		parsed = append(parsed, ps)
	}
	parsed[2].Damaged = true // mdat truncated, but its tfdt header survived
	obs := BuildObservations(parsed, 1000)
	// All four moof headers (including the damaged segment's) supply a
	// base time, so the fit window is not cut short.
	if len(obs) != 4 {
		t.Fatalf("observations = %d, want 4", len(obs))
	}
	// Reference times must advance monotonically from actual durations.
	for i := 1; i < len(obs); i++ {
		if obs[i].RefSeconds <= obs[i-1].RefSeconds {
			t.Fatalf("reference time not increasing: %+v", obs)
		}
	}
}

func TestBuildObservationsStopsAtAbsentInput(t *testing.T) {
	inputs := makeSegments(t, 3, false)
	for i := range inputs {
		inputs[i].DurationSeconds = 0.4
	}
	var parsed []*ParsedSegment
	for _, in := range inputs {
		parsed = append(parsed, New(0).ParseStage(in))
	}
	parsed[1].MP4 = nil // simulate a fully absent/unparseable segment
	obs := BuildObservations(parsed, 1000)
	if len(obs) != 1 {
		t.Fatalf("contiguous window = %d, want 1 before the gap", len(obs))
	}
}
