package bitrate

import (
	"testing"

	"mediaverify/internal/clock"
	"mediaverify/internal/errs"
)

func sec(n int64) clock.Ticks { return clock.Nanoseconds(n * 1_000_000_000) }

func TestEstimatorSlidingWindow(t *testing.T) {
	e := NewEstimator(sec(3), 3)
	// 125_000 bytes each second = 1 Mbit/s.
	for i := 1; i <= 5; i++ {
		if err := e.Add(Sample{Time: sec(int64(i)), Bytes: 125_000}); err != nil {
			t.Fatal(err)
		}
	}
	bps, degraded, reason := e.Estimate()
	if degraded {
		t.Fatalf("unexpected degradation: %s", reason)
	}
	// Window keeps the last ~4 samples; rate should be ~1 Mbit/s.
	if bps < 900_000 || bps > 1_100_000 {
		t.Fatalf("estimate %d outside 1Mbit/s tolerance", bps)
	}
}

func TestEstimatorDegradedOnInsufficientSamples(t *testing.T) {
	e := NewEstimator(sec(10), 4)
	_ = e.Add(Sample{Time: sec(1), Bytes: 1000})
	bps, degraded, reason := e.Estimate()
	if !degraded || bps != 0 || reason == "" {
		t.Fatalf("want degraded with reason, got bps=%d degraded=%v reason=%q", bps, degraded, reason)
	}
}

func TestAllocatorLadderValidation(t *testing.T) {
	if _, err := NewAllocator([]int64{400000}, 3); errs.Class(err) != errs.KindInvalidInput {
		t.Fatalf("single rung must fail: %v", err)
	}
	if _, err := NewAllocator([]int64{400000, 400000, 900000}, 3); errs.Class(err) != errs.KindInvalidInput {
		t.Fatalf("duplicate rung must fail: %v", err)
	}
}

func TestAllocatorDeviationWithin15Percent(t *testing.T) {
	a, err := NewAllocator([]int64{400_000, 900_000, 1_800_000}, 3)
	if err != nil {
		t.Fatal(err)
	}
	// Estimate 1 Mbit/s: active rung is 900k; every active allocation must
	// stay inside the 15% band.
	d := a.Allocate(0, 1_000_000, false, "")
	for _, r := range d.Renditions {
		if pct := ErrorPct(r); pct > MaxDeviationPct {
			t.Fatalf("rung %d deviation %.2f%% exceeds 15%%", r.TargetBPS, pct)
		}
	}
	active := d.Renditions[1]
	if active.ActualBPS != 1_000_000 {
		t.Fatalf("estimate inside band should pass through, got %d", active.ActualBPS)
	}
	// Estimate far below the rung is clamped to the -15% floor.
	d2 := a.Allocate(1, 100_000, false, "")
	if pct := ErrorPct(d2.Renditions[0]); pct > MaxDeviationPct {
		t.Fatalf("floor clamp broke deviation: %.2f%%", pct)
	}
	if d2.Renditions[0].ActualBPS != 340_000 {
		t.Fatalf("want floor 340000, got %d", d2.Renditions[0].ActualBPS)
	}
}

func TestHysteresisSmoothSwitching(t *testing.T) {
	a, _ := NewAllocator([]int64{400_000, 900_000, 1_800_000}, 3)
	// Start on the middle rung.
	a.Allocate(0, 900_000, false, "")
	// A modest 10% dip must not switch.
	d := a.Allocate(1, 810_000, false, "")
	if d.Switched {
		t.Fatal("small dip should stay on the rung via hysteresis")
	}
	// A 30% drop crosses the 20% band and moves down exactly one rung.
	d = a.Allocate(2, 600_000, false, "")
	if !d.Switched || d.Renditions[1].TargetBPS != 900_000 {
		t.Fatalf("expected downward switch, got switched=%v", d.Switched)
	}
	if active := d.Renditions[0]; active.TargetBPS != 400_000 {
		t.Fatalf("should land on lowest rung, got %d", active.TargetBPS)
	}
}

func TestDegradedFallsBackToLowestRung(t *testing.T) {
	a, _ := NewAllocator([]int64{400_000, 900_000, 1_800_000}, 3)
	d := a.Allocate(0, 0, true, "insufficient bandwidth samples for sliding window")
	if !d.Degraded || d.Reason == "" {
		t.Fatal("degraded decision must carry a reason")
	}
	// Every rung keeps its declared target in degraded mode (0% deviation).
	if d.Renditions[0].ActualBPS != 400_000 || ErrorPct(d.Renditions[1]) != 0 {
		t.Fatalf("degraded allocation invalid: %+v", d.Renditions)
	}
}

func TestDeterministicAllocation(t *testing.T) {
	estimates := []int64{900_000, 950_000, 600_000, 1_500_000, 1_700_000}
	run := func() []Decision {
		a, _ := NewAllocator([]int64{400_000, 900_000, 1_800_000}, 3)
		var out []Decision
		for i, est := range estimates {
			out = append(out, a.Allocate(i, est, false, ""))
		}
		return out
	}
	r1, r2 := run(), run()
	for i := range r1 {
		if len(r1[i].Renditions) != len(r2[i].Renditions) {
			t.Fatalf("segment %d nondeterministic length", i)
		}
		for j := range r1[i].Renditions {
			if r1[i].Renditions[j] != r2[i].Renditions[j] {
				t.Fatalf("segment %d rendition %d differs across runs", i, j)
			}
		}
	}
}
