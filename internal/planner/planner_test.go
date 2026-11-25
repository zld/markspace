package planner

import (
	"math"
	"testing"

	"rtpregress/internal/media"
)

func baseProblem() Problem {
	return Problem{Events: 12, TextBytes: 380, DurationNs: 2_000_000_000}
}

func TestChoosePrefersQualityThenBudgetThenBytes(t *testing.T) {
	p := baseProblem()
	cons := Constraints{BudgetMS: 500, MaxBytes: 4096, QualityMin: 0.95}
	plan, err := Choose(p, cons)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Quality < cons.QualityMin {
		t.Fatalf("quality = %.3f below floor", plan.Quality)
	}
	if plan.CostMS > cons.BudgetMS {
		t.Fatalf("cost = %.1f over budget", plan.CostMS)
	}
	if plan.EstimatedBytes > cons.MaxBytes {
		t.Fatalf("bytes = %d over cap", plan.EstimatedBytes)
	}
	// The highest-quality candidate that also fits must win.
	bestQ := 0.0
	for _, c := range DefaultCandidates() {
		if Quality(c) < cons.QualityMin {
			continue
		}
		if CostMS(p, c) > cons.BudgetMS || EstimatedBytes(p, c) > cons.MaxBytes {
			continue
		}
		if Quality(c) > bestQ {
			bestQ = Quality(c)
		}
	}
	if math.Abs(plan.Quality-bestQ) > 1e-9 {
		t.Fatalf("selected quality %.4f, best feasible %.4f", plan.Quality, bestQ)
	}
}

func TestChooseDeterministic(t *testing.T) {
	p := baseProblem()
	cons := Constraints{BudgetMS: 500, MaxBytes: 4096, QualityMin: 0.95}
	a, err := Choose(p, cons)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Choose(p, cons)
	if err != nil {
		t.Fatal(err)
	}
	if *a != *b {
		t.Fatalf("non-deterministic: %+v vs %+v", a, b)
	}
}

func TestChooseRefusesUnachievableQuality(t *testing.T) {
	p := baseProblem()
	_, err := Choose(p, Constraints{BudgetMS: 500, MaxBytes: 4096, QualityMin: 1.01})
	// 1.01 is out of range and rejected as invalid rather than infeasible.
	if err == nil || media.KindOf(err) != media.KindBudget {
		t.Fatalf("expected budget error for out-of-range floor, got %v", err)
	}

	// The best candidate scores below 1.0, so a 1.0 floor is genuinely
	// infeasible: the planner must refuse rather than silently lower the bar.
	_, err = Choose(p, Constraints{BudgetMS: 500, MaxBytes: 4096, QualityMin: 1.0})
	if err == nil {
		t.Fatal("expected refusal at unreachable quality floor")
	}
	if media.KindOf(err) != media.KindBudget {
		t.Fatalf("kind = %s", media.KindOf(err))
	}
	if msg := err.Error(); !contains(msg, "quality floor") {
		t.Fatalf("error should mention the quality floor: %s", msg)
	}
}

func TestChooseRefusesWhenBudgetImpossible(t *testing.T) {
	p := Problem{Events: 1000, TextBytes: 20000, DurationNs: 60_000_000_000}
	_, err := Choose(p, Constraints{BudgetMS: 1, MaxBytes: 100000, QualityMin: 0.8})
	if err == nil {
		t.Fatal("expected budget refusal")
	}
	if media.KindOf(err) != media.KindBudget {
		t.Fatalf("kind = %s", media.KindOf(err))
	}
	if msg := err.Error(); !contains(msg, "budget") {
		t.Fatalf("error should name the budget: %s", msg)
	}
}

func TestChooseRefusesWhenBytesImpossible(t *testing.T) {
	p := baseProblem()
	_, err := Choose(p, Constraints{BudgetMS: 500, MaxBytes: 64, QualityMin: 0.3})
	if err == nil {
		t.Fatal("expected byte-cap refusal")
	}
	if media.KindOf(err) != media.KindBudget {
		t.Fatalf("kind = %s", media.KindOf(err))
	}
	if msg := err.Error(); !contains(msg, "max-bytes") {
		t.Fatalf("error should name the byte cap: %s", msg)
	}
}

func TestChooseRejectsInvalidConstraints(t *testing.T) {
	p := baseProblem()
	for name, cons := range map[string]Constraints{
		"zero bytes":      {BudgetMS: 500, MaxBytes: 0, QualityMin: 0.5},
		"negative time":   {BudgetMS: -1, MaxBytes: 4096, QualityMin: 0.5},
		"quality too low": {BudgetMS: 500, MaxBytes: 4096, QualityMin: -0.1},
	} {
		if _, err := Choose(p, cons); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

func TestQuantize(t *testing.T) {
	p := &Plan{Candidate: Candidate{PrecisionUS: 20}}
	cases := map[int64]int64{
		0:             0,
		9_000:         0,
		10_000:        20_000,
		11_000:        20_000,
		29_000:        20_000,
		30_000:        40_000,
		1_000_000_000: 1_000_000_000,
	}
	for ns, want := range cases {
		if got := p.QuantizeNs(ns); got != want {
			t.Fatalf("quantize(%d) = %d, want %d", ns, got, want)
		}
	}
	if p.QuantizeDeviationNs() != 10_000 {
		t.Fatalf("deviation = %d", p.QuantizeDeviationNs())
	}
}

func TestSegmentCount(t *testing.T) {
	cases := []struct {
		durNs, segMS int64
		want         int
	}{
		{1_000_000_000, 500, 2},
		{1_000_000_000, 1000, 1},
		{2_500_000_000, 1000, 3},
		{0, 500, 1},
		{1_000_000_000, 0, 1},
	}
	for _, c := range cases {
		if got := SegmentCount(c.durNs, int(c.segMS)); got != c.want {
			t.Fatalf("SegmentCount(%d,%d) = %d, want %d", c.durNs, c.segMS, got, c.want)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) <= len(s) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
