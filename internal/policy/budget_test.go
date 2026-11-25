package policy

import "testing"

func goodStats() Stats {
	return Stats{
		DurationMS: 4000, Width: 1920, Height: 1080, FPS: 30,
		SampleCount: 120, KeyframeCount: 4, MaxKeyframeGapMS: 1000,
		VideoBytes: 4_000_000, // 8 Mbps
		AudioBytes: 96_000,
	}
}

func TestQualityAndFeasibleDecision(t *testing.T) {
	s := goodStats()
	score := QualityScore(s)
	if score < 0.92 {
		t.Fatalf("expected high quality proxy, got %.4f", score)
	}
	d := Evaluate("sha256:good", s, Target{
		Width: 1920, Height: 1080, FPS: 30, QualityMin: 0.92,
		BudgetMbps: 18, AudioKbps: 192,
	})
	if !d.Feasible || !d.QualityPass {
		t.Fatalf("expected feasible decision: %+v", d)
	}
	if d.TargetTotalKbps > d.MaximumTotalKbps {
		t.Fatalf("target %d exceeds budget %d", d.TargetTotalKbps, d.MaximumTotalKbps)
	}
	if !WithinBudgetTolerance(d.TargetTotalKbps, 8192, 0.08) {
		t.Fatal("expected actual fixture bitrate near target")
	}
}

func TestBudgetImpossibleReportsDeviations(t *testing.T) {
	s := goodStats()
	s.Width, s.Height, s.FPS = 7680, 4320, 60
	d := Evaluate("sha256:big", s, Target{
		Width: 7680, Height: 4320, FPS: 60, QualityMin: 0.92, BudgetMbps: 18,
	})
	if d.Feasible {
		t.Fatalf("expected infeasible decision: %+v", d)
	}
	if d.BudgetDeviationMbps <= 200 {
		t.Fatalf("expected large budget overrun, got %.3f", d.BudgetDeviationMbps)
	}
	found := false
	for _, dev := range d.Deviations {
		if dev.Constraint == "bitrate_budget" && dev.Delta > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing budget deviation: %+v", d.Deviations)
	}
}

func TestSparseKeyframesLowerQuality(t *testing.T) {
	regular := goodStats()
	sparse := goodStats()
	sparse.KeyframeCount = 1
	sparse.MaxKeyframeGapMS = 12000
	if QualityScore(sparse) >= QualityScore(regular) {
		t.Fatal("sparse keyframes must not score higher than regular keyframes")
	}
	d := Evaluate("sha256:sparse", sparse, Target{QualityMin: 0.92, BudgetMbps: 18})
	if d.Feasible {
		t.Fatalf("sparse boundary should fail strict quality: %+v", d)
	}
}
