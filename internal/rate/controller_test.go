package rate

import (
	"math"
	"testing"
)

func TestAllocateExactSum(t *testing.T) {
	total := int64(10_000_000)
	c := []float64{2, 8, 5, 1, 10}
	fc := []int{96, 96, 96, 48, 96}
	mins := []int64{1000, 1000, 1000, 500, 1000}
	b, err := Allocate(total, c, fc, mins)
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	var sum int64
	for i, v := range b {
		sum += v
		if v < mins[i] {
			t.Errorf("segment %d budget %d below floor %d", i, v, mins[i])
		}
	}
	if sum != total {
		t.Errorf("budgets sum %d != total %d", sum, total)
	}
}

func TestAllocateInfeasible(t *testing.T) {
	mins := []int64{5000, 5000, 5000}
	_, err := Allocate(9000, []float64{1, 1, 1}, []int{10, 10, 10}, mins)
	if err == nil {
		t.Fatal("expected AllocationError when floors exceed total")
	}
	if _, ok := err.(*AllocationError); !ok {
		t.Fatalf("expected *AllocationError, got %T", err)
	}
}

func TestAllocateLargeRoundingGap(t *testing.T) {
	// Budgets initially far above target force the negative rebalance path
	// with a delta much larger than n*2, which an earlier guard mishandled.
	total := int64(1_000_000)
	n := 6
	c := make([]float64, n)
	fc := make([]int, n)
	mins := make([]int64, n)
	for i := 0; i < n; i++ {
		c[i], fc[i], mins[i] = 1, 96, 100
	}
	b, err := Allocate(total, c, fc, mins)
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	var sum int64
	for _, v := range b {
		sum += v
	}
	if sum != total {
		t.Errorf("sum %d != %d", sum, total)
	}
}

func TestControllerConvergence(t *testing.T) {
	c := Controller{TargetBits: 100_000, DurationSec: 1, TolerancePct: 8, MaxIterations: 5, MinKbps: 40, MaxAdjustRatio: 2}
	kbps := c.InitialKbps()
	if kbps != 100 {
		t.Errorf("initial kbps = %d, want 100", kbps)
	}
	// First attempt overshoots by 25%.
	next, dev, converged := c.NextKbps(100, 125_000)
	if converged || math.Abs(dev-25) > 0.01 {
		t.Errorf("deviation=%.2f converged=%v", dev, converged)
	}
	if next >= 100 {
		t.Errorf("overshoot should lower bitrate, got %d", next)
	}
	// An attempt within tolerance stops the loop.
	_, _, converged = c.NextKbps(100, 105_000)
	if !converged {
		t.Error("5%% deviation should be within 8%% tolerance")
	}
}

func TestControllerClampsAdjustment(t *testing.T) {
	c := Controller{TargetBits: 100_000, DurationSec: 1, TolerancePct: 1, MaxIterations: 5, MinKbps: 40, MaxAdjustRatio: 2}
	// Huge undershoot: unclamped correction would more than double the rate.
	next, _, converged := c.NextKbps(100, 10_000)
	if converged {
		t.Error("should not converge on 90%% undershoot")
	}
	if next > 200 {
		t.Errorf("correction %d exceeds 2x clamp", next)
	}
}
