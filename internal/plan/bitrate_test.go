package plan

import (
	"errors"
	"math"
	"testing"
)

func TestValidateRejectsBadInputs(t *testing.T) {
	good := []float64{1, 2, 3}
	cases := []struct {
		name                       string
		duration, seg, target, min float64
		samples                    []float64
	}{
		{"zero duration", 0, 10, 2400, 100, good},
		{"negative segment", 120, -10, 2400, 100, good},
		{"nan complexity", 120, 10, 2400, 100, []float64{1, math.NaN()}},
		{"inf complexity", 120, 10, 2400, 100, []float64{1, math.Inf(1)}},
		{"negative complexity", 120, 10, 2400, 100, []float64{1, -0.5}},
		{"empty samples", 120, 10, 2400, 100, nil},
		{"segment larger than duration", 5, 10, 2400, 100, good},
		{"zero target", 120, 10, 0, 100, good},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(Validate(tc.duration, tc.seg, tc.target, tc.min, tc.samples), ErrInvalidInput) {
				t.Fatal("expected ErrInvalidInput")
			}
		})
	}
}

func TestPlanBudgetWithinOnePercent(t *testing.T) {
	// Deterministic complexity pattern: quiet start, busy middle, quiet end.
	samples := make([]float64, 120)
	for i := range samples {
		switch {
		case i < 30:
			samples[i] = 0.2
		case i < 90:
			samples[i] = 3 + math.Sin(float64(i)*0.3)
		default:
			samples[i] = 0.4
		}
	}
	specs, err := Plan(120, 10, 2400, 100, samples)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 12 {
		t.Fatalf("want 12 segments, got %d", len(specs))
	}
	if errPct := BudgetError(2400, 120, specs); errPct > 1.0 {
		t.Fatalf("budget error %.4f%% exceeds 1%%", errPct)
	}
	// Busy middle segments must get more bitrate than the quiet ends.
	midAvg := (specs[5].TargetKbps + specs[6].TargetKbps) / 2
	endAvg := (specs[0].TargetKbps + specs[11].TargetKbps) / 2
	if !(midAvg > endAvg) {
		t.Fatalf("expected complex segments to outrank static ones: mid=%.1f end=%.1f", midAvg, endAvg)
	}
	// Determinism: same inputs produce the same plan.
	specs2, _ := Plan(120, 10, 2400, 100, samples)
	for i := range specs {
		if specs[i].TargetKbps != specs2[i].TargetKbps {
			t.Fatalf("nondeterministic plan at segment %d", i)
		}
	}
}

func TestPlanInfeasibleSegments(t *testing.T) {
	samples := make([]float64, 40)
	for i := range samples {
		samples[i] = float64(i%3 + 1) // lopsided but finite
	}
	// A minimum above the average forces at least one low-complexity
	// segment under the floor.
	specs, err := Plan(40, 10, 300, 500, samples)
	if err != nil {
		t.Fatal(err)
	}
	flagged := 0
	for _, s := range specs {
		if !s.Feasible {
			flagged++
			if s.InfeasibleReason == "" {
				t.Fatal("infeasible segment lacks reason")
			}
		}
	}
	if flagged == 0 {
		t.Fatal("expected at least one infeasible segment")
	}
}

func TestPlanHandlesTrailingPartialSegment(t *testing.T) {
	samples := make([]float64, 25)
	for i := range samples {
		samples[i] = 1
	}
	specs, err := Plan(25, 10, 1000, 50, samples)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 3 || math.Abs(specs[2].DurationSec-5) > 1e-9 {
		t.Fatalf("unexpected segmentation: %+v", specs)
	}
	if errPct := BudgetError(1000, 25, specs); errPct > 1.0 {
		t.Fatalf("budget error %.4f%%", errPct)
	}
}
