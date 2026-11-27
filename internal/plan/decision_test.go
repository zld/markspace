package plan

import (
	"testing"

	"mediad/internal/model"
)

func sampleCandidates() []Candidate {
	return []Candidate{
		{Name: "low", BitrateBPS: 64_000, Quality: 0.80, EstCPUNanos: 1_000, EstBytes: 8_000},
		{Name: "mid", BitrateBPS: 128_000, Quality: 0.92, EstCPUNanos: 2_000, EstBytes: 16_000},
		{Name: "high", BitrateBPS: 192_000, Quality: 0.97, EstCPUNanos: 4_000, EstBytes: 24_000},
	}
}

func TestChoosePicksSmallestFeasible(t *testing.T) {
	d, err := Choose(sampleCandidates(), Constraints{MinQuality: 0.90, TargetBitrate: 128_000})
	if err != nil || !d.Feasible {
		t.Fatalf("unexpected: %v %+v", err, d)
	}
	if d.Candidate.Name != "mid" {
		t.Fatalf("want mid (smallest above quality 0.90), got %s", d.Candidate.Name)
	}
	if d.Action != ActionKeep {
		t.Fatalf("target-bitrate match should keep, got %s", d.Action)
	}
	if d.Algorithm != AlgorithmVersion {
		t.Fatalf("algorithm id = %q", d.Algorithm)
	}
}

func TestQualityGateNeverRelaxed(t *testing.T) {
	// Even with a tiny size cap that only "low" fits, quality 0.95 must win
	// over size: low is rejected on quality, mid/high on size -> infeasible.
	_, err := Choose(sampleCandidates(), Constraints{MinQuality: 0.95, MaxDeliveryB: 10})
	if model.AsError(err).Code != model.ErrPlanInfeasible {
		t.Fatalf("want ErrPlanInfeasible, got %v", err)
	}
}

func TestBudgetPriorityOverSize(t *testing.T) {
	// CPU budget rules out high; mid still fits and beats nothing else.
	d, err := Choose(sampleCandidates(), Constraints{MinQuality: 0.90, CPUBudgetNS: 3_000})
	if err != nil {
		t.Fatal(err)
	}
	if d.Candidate.Name != "mid" {
		t.Fatalf("got %s", d.Candidate.Name)
	}
	// All quality-passing profiles blow the CPU budget.
	_, err = Choose(sampleCandidates(), Constraints{MinQuality: 0.90, CPUBudgetNS: 1_500})
	if model.AsError(err).Code != model.ErrPlanInfeasible {
		t.Fatalf("want infeasible on CPU, got %v", err)
	}
}

func TestByteBudgetAndDeliveryCap(t *testing.T) {
	if _, err := Choose(sampleCandidates(), Constraints{MinQuality: 0.80, ByteBudget: 7_999}); model.AsError(err).Code != model.ErrPlanInfeasible {
		t.Fatalf("byte budget must reject all: %v", err)
	}
	d, err := Choose(sampleCandidates(), Constraints{MinQuality: 0.80, MaxDeliveryB: 8_000})
	if err != nil || d.Candidate.Name != "low" {
		t.Fatalf("delivery cap should leave only low: %v %+v", err, d)
	}
}

func TestRejectReasonsDocumented(t *testing.T) {
	d, _ := Choose(sampleCandidates(), Constraints{MinQuality: 0.95, MaxDeliveryB: 10})
	gate := map[string]bool{}
	for _, r := range d.Rejects {
		gate[r.Gate] = true
	}
	if !gate["quality"] || !gate["delivery_size"] {
		t.Fatalf("reject reasons missing gates: %+v", d.Rejects)
	}
	if d.Action != ActionReject {
		t.Fatalf("infeasible decision must be reject, got %s", d.Action)
	}
}

func TestBudgetGuardStopsStage(t *testing.T) {
	g := NewGuard(Budget{MaxCPUNanos: 100, MaxBytes: 50})
	g.Add(60, 10)
	if err := g.Check("normalize"); err != nil {
		t.Fatalf("within limits: %v", err)
	}
	g.Add(50, 0) // CPU now 110 > 100
	if model.AsError(g.Check("normalize")).Code != model.ErrBudgetExceeded {
		t.Fatal("CPU ceiling must stop the stage")
	}
	g2 := NewGuard(Budget{MaxBytes: 5})
	g2.Add(0, 6)
	if model.AsError(g2.Check("export")).Code != model.ErrBudgetExceeded {
		t.Fatal("byte ceiling must stop the stage")
	}
}

func TestEmptyCandidates(t *testing.T) {
	if _, err := Choose(nil, Constraints{}); model.AsError(err).Code != model.ErrPlanInfeasible {
		t.Fatalf("got %v", err)
	}
}
