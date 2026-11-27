package budget

import (
	"strings"
	"testing"

	"mediapipeline/internal/fault"
)

func TestPlanWithinBudget(t *testing.T) {
	p, err := NewPlanner(DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	plan := p.Plan(Input{BandwidthBps: 8_000_000, RemainingBytes: 4 << 20, RemainingUnits: 400})
	if plan.Rejected {
		t.Fatalf("healthy plan rejected: %s", plan.Reason)
	}
	if plan.WindowMS < DefaultLimits.MinWindowMS || plan.WindowMS > DefaultLimits.MaxWindowMS {
		t.Fatalf("window %d out of bounds", plan.WindowMS)
	}
	if plan.Concurrency < DefaultLimits.MinConcurrency || plan.Concurrency > DefaultLimits.MaxConcurrency {
		t.Fatalf("concurrency %d out of bounds", plan.Concurrency)
	}
	if plan.ByteBudget <= 0 || plan.ByteBudget > plan.EstBytes {
		t.Fatalf("byte budget %d not bounded by estimate %d", plan.ByteBudget, plan.EstBytes)
	}
	if plan.Tasks != EstimateTasks(4<<20, DefaultLimits.MaxBytesPerTask) {
		t.Fatalf("tasks %d disagrees with shared rule", plan.Tasks)
	}
}

func TestPlanBandwidthDropRejected(t *testing.T) {
	p, _ := NewPlanner(DefaultLimits)
	// 64 MiB at 400 kB/s takes ~160 s, far beyond the 5 s live window.
	plan := p.Plan(Input{BandwidthBps: 400_000, RemainingBytes: 64 << 20, RemainingUnits: 100})
	if !plan.Rejected {
		t.Fatal("bandwidth drop must be rejected")
	}
	if len(plan.Faults) == 0 || plan.Faults[0].Code != fault.CodeBudgetExceeded {
		t.Fatalf("faults = %+v", plan.Faults)
	}
	if !strings.Contains(plan.Reason, "live-window cap") {
		t.Fatalf("reason lacks cause: %q", plan.Reason)
	}
}

func TestPlanByteBudgetRejected(t *testing.T) {
	lim := DefaultLimits
	lim.MaxBudgetBytes = 1024
	p, _ := NewPlanner(lim)
	plan := p.Plan(Input{BandwidthBps: 8_000_000, RemainingBytes: 1 << 20, RemainingUnits: 10})
	if !plan.Rejected || len(plan.Faults) == 0 {
		t.Fatal("byte budget overflow must be rejected")
	}
	if plan.Faults[0].Code != fault.CodeBudgetExceeded {
		t.Fatalf("code = %s", plan.Faults[0].Code)
	}
}

func TestPlanUnitLimitRejected(t *testing.T) {
	lim := DefaultLimits
	lim.MaxUnits = 10
	p, _ := NewPlanner(lim)
	plan := p.Plan(Input{BandwidthBps: 8_000_000, RemainingBytes: 1000, RemainingUnits: 11})
	if !plan.Rejected || plan.Faults[0].Code != fault.CodeLimitExceeded {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanGapPenaltyLowersEstimate(t *testing.T) {
	p, _ := NewPlanner(DefaultLimits)
	clean := p.Plan(Input{BandwidthBps: 8_000_000, RemainingBytes: 1 << 20})
	gappy := p.Plan(Input{BandwidthBps: 8_000_000, RemainingBytes: 1 << 20, Gaps: 4})
	if gappy.EffectiveBps >= clean.EffectiveBps {
		t.Fatalf("gaps did not lower effective bandwidth: %d >= %d",
			gappy.EffectiveBps, clean.EffectiveBps)
	}
	if gappy.Rejected {
		t.Fatalf("four gaps must not reject: %s", gappy.Reason)
	}
}

func TestPlanNonPositiveBandwidth(t *testing.T) {
	p, _ := NewPlanner(DefaultLimits)
	plan := p.Plan(Input{BandwidthBps: 0, RemainingBytes: 1})
	if !plan.Rejected || plan.Faults[0].Code != fault.CodeBudgetExceeded {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanEmptyWork(t *testing.T) {
	p, _ := NewPlanner(DefaultLimits)
	plan := p.Plan(Input{BandwidthBps: 1_000_000})
	if plan.Rejected || plan.Concurrency != 0 || plan.Tasks != 0 {
		t.Fatalf("empty work plan = %+v", plan)
	}
}

func TestNewPlannerRejectsBadLimits(t *testing.T) {
	bad := DefaultLimits
	bad.MaxConcurrency = 0
	if _, err := NewPlanner(bad); err == nil {
		t.Fatal("expected error for zero concurrency")
	}
	bad = DefaultLimits
	bad.MaxBytesPerTask = 0
	if _, err := NewPlanner(bad); err == nil {
		t.Fatal("expected error for zero per-task bytes")
	}
}
