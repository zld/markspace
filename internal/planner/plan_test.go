package planner

import (
	"errors"
	"testing"

	"medialab/internal/model"
)

func baseInput() Input {
	return Input{
		BaseBytesPerFrame: 4096,
		BaseCostMS:        2,
		TotalFrames:       60,
		OverheadBytes:     512,
	}
}

func TestChooseSmallestFeasible(t *testing.T) {
	in := baseInput()
	plan, err := Choose(in, Budget{
		MaxBytes: 200_000, MaxCostMS: 500, MinQuality: 0.7,
		FPSChoices: []int{1, 2, 4}, QualityChoices: []float64{1, 0.875, 0.75},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Chosen == nil {
		t.Fatal("no plan")
	}
	if plan.Chosen.Quality < 0.7 {
		t.Fatalf("chosen quality %f below floor", plan.Chosen.Quality)
	}
	if plan.Chosen.CostMS > 500 || plan.Chosen.Bytes > 200_000 {
		t.Fatalf("chosen outside budget: %+v", plan.Chosen)
	}
	for _, c := range plan.Feasible {
		if c.Bytes < plan.Chosen.Bytes {
			t.Fatalf("smaller feasible candidate ignored: %+v", c)
		}
	}
}

func TestChooseRejectsLowQuality(t *testing.T) {
	plan, err := Choose(baseInput(), Budget{
		MaxBytes: 1 << 30, MaxCostMS: 1 << 20, MinQuality: 0.9,
		QualityChoices: []float64{0.5, 0.6},
	})
	var conflict *ErrConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v", err)
	}
	if plan.Conflict.Reason != model.CodeQualityFloor {
		t.Fatalf("reason = %s", plan.Conflict.Reason)
	}
	if len(plan.Conflict.Evaluated) == 0 {
		t.Fatal("no candidate summary")
	}
	for _, c := range plan.Conflict.Evaluated {
		if c.Quality >= 0.9 {
			t.Fatalf("impossible candidate listed: %+v", c)
		}
	}
}

func TestChooseBudgetConflict(t *testing.T) {
	plan, err := Choose(Input{
		BaseBytesPerFrame: 1 << 20, BaseCostMS: 100, TotalFrames: 100, OverheadBytes: 1024,
	}, Budget{
		MaxBytes: 1 << 30, MaxCostMS: 50, MinQuality: 0.1,
		FPSChoices: []int{1}, QualityChoices: []float64{0.5},
	})
	var conflict *ErrConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v", err)
	}
	if plan.Conflict.Reason != model.CodeBudgetConflict {
		t.Fatalf("reason = %s", plan.Conflict.Reason)
	}
	if len(plan.Conflict.Evaluated) == 0 || plan.Conflict.Evaluated[0].CostMS == 0 {
		t.Fatal("evaluation summary missing costs")
	}
}

func TestChooseDeterministic(t *testing.T) {
	b := Budget{MaxBytes: 500_000, MaxCostMS: 1000, MinQuality: 0.5}
	a1, err := Choose(baseInput(), b)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := Choose(baseInput(), b)
	if err != nil {
		t.Fatal(err)
	}
	if *a1.Chosen != *a2.Chosen {
		t.Fatalf("non-deterministic choice: %+v vs %+v", a1.Chosen, a2.Chosen)
	}
}

func TestChooseRejectsEmptyGrid(t *testing.T) {
	_, err := Choose(Input{TotalFrames: 0}, Budget{MaxBytes: 1, MaxCostMS: 1})
	if err == nil {
		t.Fatal("empty input accepted")
	}
	if _, err := Choose(baseInput(), Budget{MaxBytes: 0, MaxCostMS: 1}); err == nil {
		t.Fatal("zero budget accepted")
	}
}

func TestScaledBytesMonotone(t *testing.T) {
	prev := int64(0)
	for _, step := range []int{1, 4, 16, 64, 128, 255} {
		b := scaledBytes(4096, step)
		if b > 4096 || b < 4096*15/100 {
			t.Fatalf("step %d bytes %d outside bounds", step, b)
		}
		if prev != 0 && b > prev {
			t.Fatalf("bytes not monotone at step %d: %d > %d", step, b, prev)
		}
		prev = b
	}
}
