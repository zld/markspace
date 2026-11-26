package planner

import (
	"testing"

	"pwflow/internal/fault"
	"pwflow/internal/timebase"
)

func TestEvaluateAcceptsFixtureBudget(t *testing.T) {
	plan, err := Evaluate(Request{
		BudgetMiB: 64, Workers: 4, InputBytes: 500_000,
		Windows: []WindowEstimate{{Index: 0, Interval: timebase.Interval{Lo: 0, Hi: 5_000_000_000}, FPS: 30}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Workers != 4 || plan.EstimatedMemoryBytes > plan.BudgetBytes {
		t.Fatalf("bad plan: %+v", plan)
	}
	if len(plan.CandidateScores) != 1 {
		t.Fatalf("scores=%+v", plan.CandidateScores)
	}
}

func TestEvaluateRejectsConcurrencyOverBudget(t *testing.T) {
	_, err := Evaluate(Request{BudgetMiB: 64, Workers: 100})
	if !fault.IsCode(err, fault.BudgetExceeded) {
		t.Fatalf("err=%v, want budget exceeded", err)
	}

	plan, err := Evaluate(Request{BudgetMiB: 64, Workers: 100, AutoWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Workers >= 100 || plan.Workers < 1 {
		t.Fatalf("auto workers=%d", plan.Workers)
	}
}

func TestEvaluateRejectsImpossibleMinimum(t *testing.T) {
	_, err := Evaluate(Request{BudgetMiB: 1, Workers: 1})
	if !fault.IsCode(err, fault.BudgetExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestBandwidthEstimateAndChunkNeverExceedBudget(t *testing.T) {
	var b BandwidthTracker
	for i := 0; i < 4; i++ {
		b.ObserveArrival(int64(i)*20_000_000, 12500) // ~5 Mbps
	}
	if b.BpsEWMA <= 0 {
		t.Fatal("bandwidth estimate not updated")
	}
	max := int64(1 << 20)
	chunk := AdaptiveChunk(b.BpsEWMA, timebase.Seconds(2), max)
	if chunk <= 0 || chunk > max {
		t.Fatalf("adaptive chunk=%d outside [1,%d]", chunk, max)
	}
	if got := AdaptiveChunk(0, timebase.Seconds(1), max); got != max/2 {
		t.Fatalf("zero bandwidth chunk=%d", got)
	}
}
