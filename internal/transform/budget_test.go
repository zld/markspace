package transform

import (
	"errors"
	"testing"

	"medpipe/internal/fault"
)

func TestDecidePriorityQualityBudgetThenBytes(t *testing.T) {
	opts := []Option{
		{Name: "small-low-quality", Quality: 80, EstimateMS: 1, Bytes: 10},
		{Name: "quality-fast-large", Quality: 100, EstimateMS: 5, Bytes: 1000},
		{Name: "quality-slow-small", Quality: 100, EstimateMS: 50, Bytes: 100},
		{Name: "quality-fast-small", Quality: 99.5, EstimateMS: 4, Bytes: 200},
	}
	d, err := Decide(opts, Constraints{MinQuality: 99, BudgetMS: 10, MaxBytes: 10000})
	if err != nil {
		t.Fatal(err)
	}
	// slow-small is outside processing budget; fast-small is smallest affordable.
	if d.Selected.Name != "quality-fast-small" {
		t.Fatalf("selected %+v", d.Selected)
	}
}

func TestDecideFailsWhenQualityUnreachable(t *testing.T) {
	_, err := Decide([]Option{{Name: "low", Quality: 90, EstimateMS: 1, Bytes: 1}},
		Constraints{MinQuality: 99, BudgetMS: 100})
	assertCode(t, err, "quality_gate_unreachable")
}

func TestDecideFailsWhenQualityCannotFitProcessingBudget(t *testing.T) {
	_, err := Decide([]Option{{Name: "slow", Quality: 100, EstimateMS: 500}},
		Constraints{MinQuality: 99, BudgetMS: 10})
	assertCode(t, err, "processing_budget_unreachable")
}

func TestDecideFailsWhenByteCapCannotFit(t *testing.T) {
	_, err := Decide([]Option{{Name: "big", Quality: 100, EstimateMS: 1, Bytes: 2048}},
		Constraints{MinQuality: 99, BudgetMS: 10, MaxBytes: 1024})
	assertCode(t, err, "delivery_budget_unreachable")
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != code || fe.Stage != fault.StageBudget {
		t.Fatalf("expected budget %s, got %v", code, err)
	}
}
