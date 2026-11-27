package transform

import (
	"testing"

	"rtx/internal/model"
)

func baseInput() Input {
	return Input{
		MinQuality:          0.5,
		MeasuredQuality:     0.9,
		TargetBitrateBPS:    128_000,
		EstimatedBitrateBPS: 120_000,
		MaxBitrateBPS:       256_000,
		BudgetMS:            120,
		EstimatedCostMS:     40,
		DriftPPM:            120,
		MaxDriftPPM:         300,
	}
}

func TestGenerateFeasible(t *testing.T) {
	c := Generate(baseInput())
	if !c.Feasible {
		t.Fatalf("want feasible, got %+v", c.Constraints)
	}
	if len(c.Constraints) != 4 {
		t.Fatalf("want 4 constraints, got %d", len(c.Constraints))
	}
	for _, con := range c.Constraints {
		if !con.Pass {
			t.Fatalf("constraint %s should pass: %+v", con.Name, con)
		}
		if con.Deviation != con.Measured-con.Threshold {
			t.Fatalf("constraint %s deviation inconsistent", con.Name)
		}
	}
}

func TestGenerateBudgetInfeasible(t *testing.T) {
	in := baseInput()
	in.BudgetMS = 1
	c := Generate(in)
	if c.Feasible {
		t.Fatal("budget 1ms with 40ms cost must be infeasible")
	}
	if !c.BudgetExceeded() {
		t.Fatal("BudgetExceeded must be true")
	}
	var found bool
	for _, a := range c.Anomalies {
		if a.Reason == model.ReasonConstraint && a.Stage == model.StageTransform {
			found = true
		}
	}
	if !found {
		t.Fatal("constraint anomaly missing")
	}
}

func TestGenerateQualityFailure(t *testing.T) {
	in := baseInput()
	in.MeasuredQuality = 0.2
	c := Generate(in)
	if c.Feasible {
		t.Fatal("quality below threshold must be infeasible")
	}
	if c.BudgetExceeded() {
		t.Fatal("budget passes here; only quality fails")
	}
}

func TestGenerateDriftFailureUsesAbsolute(t *testing.T) {
	in := baseInput()
	in.DriftPPM = -450
	c := Generate(in)
	if c.Feasible {
		t.Fatal("negative drift beyond threshold must fail")
	}
	for _, con := range c.Constraints {
		if con.Name == "drift_ppm" && con.Measured != 450 {
			t.Fatalf("drift constraint must use absolute value: %+v", con)
		}
	}
}
