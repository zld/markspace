package rtprecover

import (
	"math"
	"testing"
	"time"

	"github.com/archive/mediakernel/internal/timeline"
)

func TestEstimatorEWMAAdaptsAndFloors(t *testing.T) {
	est, err := NewEstimator(0.5, 1000)
	if err != nil {
		t.Fatal(err)
	}
	// First sample initializes directly: 1000 bytes in 1s = 8000 bps.
	if err := est.Observe(1000, time.Second); err != nil {
		t.Fatal(err)
	}
	if got := est.Bps(); got != 8000 {
		t.Fatalf("init bps = %v, want 8000", got)
	}
	// Halving link: 500 bytes/s -> 4000 bps; EWMA(0.5) = 6000.
	if err := est.Observe(500, time.Second); err != nil {
		t.Fatal(err)
	}
	if got := est.Bps(); math.Abs(got-6000) > 1e-9 {
		t.Fatalf("ewma = %v, want 6000", got)
	}
	// Collapsed link is floored at minBps, never zero.
	if err := est.Observe(0, time.Second); err != nil {
		t.Fatal(err)
	}
	if got := est.Bps(); got < 1000 {
		t.Fatalf("floor violated: %v", got)
	}
	if est.Samples() != 3 {
		t.Fatalf("samples = %d, want 3", est.Samples())
	}
}

func TestEstimatorRejectsBadInputs(t *testing.T) {
	if _, err := NewEstimator(0, 1000); timeline.Classify(err) != timeline.KindInvalidInput {
		t.Fatalf("alpha zero: %v", err)
	}
	est, _ := NewEstimator(0.5, 1000)
	if err := est.Observe(10, 0); timeline.Classify(err) != timeline.KindInvalidInput {
		t.Fatalf("zero interval: %v", err)
	}
}

func TestPlanBudgetsTracksChangingBandwidth(t *testing.T) {
	est, _ := NewEstimator(0.5, 500_000)
	// Start link well above target.
	_ = est.Observe(1_000_000, time.Second) // 8 Mbps
	const target = int64(2_400_000)
	budgets, err := PlanBudgets(int64(4*timeline.Second), int64(timeline.Second), target, est)
	if err != nil {
		t.Fatal(err)
	}
	if len(budgets) != 4 {
		t.Fatalf("budgets = %d, want 4", len(budgets))
	}
	// Above-target link: first budget follows the target, not the link.
	if budgets[0].PlannedBitrateBps != target {
		t.Fatalf("first rate = %d, want target %d", budgets[0].PlannedBitrateBps, target)
	}
	// First-segment bytes: 2.4 Mbps for 1 s = 300_000 bytes.
	if budgets[0].PlannedBytes != 300_000 {
		t.Fatalf("first bytes = %d, want 300000", budgets[0].PlannedBytes)
	}
	// Link degrades below target before segment 2. EWMA smoothing needs two
	// weak samples before the estimate actually crosses the target.
	_ = est.Observe(100_000, time.Second) // 800 kbps sample
	_ = est.Observe(20_000, time.Second)  // 160 kbps sample -> EWMA ~2.28 Mbps
	est2 := est
	budgets2, _ := PlanBudgets(int64(4*timeline.Second), int64(timeline.Second), target, est2)
	// New plans must be capped to the degraded estimate.
	if budgets2[0].PlannedBitrateBps >= target {
		t.Fatalf("degraded plan still targets %d", budgets2[0].PlannedBitrateBps)
	}
	// Last segment carries the remainder duration.
	if budgets[3].DurationUS != int64(timeline.Second) {
		t.Fatalf("segment duration = %d", budgets[3].DurationUS)
	}
}

func TestPlanBudgetsRoundsTailSegment(t *testing.T) {
	est, _ := NewEstimator(0.5, 1000)
	_ = est.Observe(1_000_000, time.Second)
	budgets, err := PlanBudgets(int64(2500*timeline.Millisecond), int64(timeline.Second), 8_000_000, est)
	if err != nil {
		t.Fatal(err)
	}
	if len(budgets) != 3 {
		t.Fatalf("segments = %d, want 3", len(budgets))
	}
	if budgets[2].DurationUS != int64(500*timeline.Millisecond) {
		t.Fatalf("tail duration = %d, want 500000", budgets[2].DurationUS)
	}
	// 8 Mbps for 0.5 s = 500_000 bytes.
	if budgets[2].PlannedBytes != 500_000 {
		t.Fatalf("tail bytes = %d, want 500000", budgets[2].PlannedBytes)
	}
}

func TestBitrateErrorFiniteAndSigned(t *testing.T) {
	got, err := BitrateError(2_400_000, 2_160_000) // -10%
	if err != nil || math.Abs(got+10) > 1e-9 {
		t.Fatalf("error = %v, %v", got, err)
	}
	got, _ = BitrateError(2_400_000, 2_520_000) // +5%
	if math.Abs(got-5) > 1e-9 {
		t.Fatalf("error = %v, want 5", got)
	}
	if math.IsNaN(got) || math.IsInf(got, 0) {
		t.Fatal("bitrate error must always be finite")
	}
	if _, err := BitrateError(0, 1); timeline.Classify(err) != timeline.KindInvalidInput {
		t.Fatalf("zero target: %v", err)
	}
}
