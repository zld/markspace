package quality

import (
	"testing"

	"mediacore/internal/fault"
)

func cand(name string, size int64, psnr float64, ms int64) Candidate {
	return Candidate{Name: name, Codec: "av1", SizeBytes: size, DurationSec: 10, PSNRdB: psnr, ProcessMS: ms}
}

func baseConfig() Config {
	return Config{TargetKbps: 800, QualityMin: 30, Budget: 1000, RateTolerance: DefaultRateTolerance}
}

func TestDecideChoosesSmallestPassing(t *testing.T) {
	d, err := Decide(baseConfig(), []Candidate{
		cand("large", 2_000_000, 45, 100),
		cand("small", 900_000, 33, 100),
		cand("tiny", 500_000, 22, 100),
		cand("slow", 400_000, 40, 5000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Selected == nil || d.Selected.Name != "small" || d.Reason != ReasonSelected {
		t.Fatalf("decision = %+v", d)
	}
	if d.Verdicts[2].QualityOK || d.Verdicts[3].BudgetOK {
		t.Fatalf("verdicts = %+v", d.Verdicts)
	}
}

func TestDecideNeverLowersFloor(t *testing.T) {
	d, err := Decide(baseConfig(), []Candidate{cand("bad", 800_000, 29.9, 10)})
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeNoCandidate {
		t.Fatalf("err = %v", err)
	}
	if d.Selected != nil || !d.Fallback {
		t.Fatalf("decision = %+v", d)
	}
}

func TestDecidePriorityOrder(t *testing.T) {
	// Quality beats size, size beats budget only after quality passes.
	d, err := Decide(baseConfig(), []Candidate{
		cand("fast-low", 100_000, 20, 1),
		cand("slow-high", 500_000, 40, 900),
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Selected == nil || d.Selected.Name != "slow-high" {
		t.Fatalf("selected = %+v", d.Selected)
	}
	// A smaller candidate over budget must lose to a larger one inside it.
	d, err = Decide(baseConfig(), []Candidate{
		cand("over-budget", 100_000, 40, 2000),
		cand("in-budget", 300_000, 35, 500),
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Selected == nil || d.Selected.Name != "in-budget" {
		t.Fatalf("selected = %+v", d.Selected)
	}
}

func TestDecideAllOverBudget(t *testing.T) {
	d, err := Decide(baseConfig(), []Candidate{cand("slow", 100_000, 40, 2000)})
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeBudgetExceeded || d.Selected != nil {
		t.Fatalf("err=%v decision=%+v", err, d)
	}
}

func TestDecideTieBreakDeterministic(t *testing.T) {
	a := []Candidate{cand("b", 200_000, 40, 10), cand("a", 200_000, 40, 10)}
	d1, _ := Decide(baseConfig(), a)
	d2, _ := Decide(baseConfig(), a)
	if d1.Selected.Name != "a" || d2.Selected.Name != "a" {
		t.Fatalf("tie = %s / %s", d1.Selected.Name, d2.Selected.Name)
	}
}

func TestDecideEmpty(t *testing.T) {
	if _, err := Decide(baseConfig(), nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestDecideRateOutsideToleranceStillSelected(t *testing.T) {
	d, err := Decide(baseConfig(), []Candidate{cand("off", 2_000_000, 40, 10)})
	if err != nil {
		t.Fatal(err)
	}
	if d.Selected == nil || d.Reason != ReasonSelectedNoRate || d.Verdicts[0].RateOK {
		t.Fatalf("decision = %+v", d)
	}
}
