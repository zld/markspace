package fanout

import (
	"testing"

	"mediasync/internal/clock"
	"mediasync/internal/fixture"
)

func buildLadder(t *testing.T, scenario fixture.Scenario, bitrates []int) *Ladder {
	t.Helper()
	cfg, err := fixture.DefaultConfig(41, 3_000_000_000, bitrates)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DriftPPM = 250
	if scenario == fixture.CaseWrap {
		cfg.ForceWrap = true
	}
	stream, err := fixture.Generate(cfg, scenario)
	if err != nil {
		t.Fatal(err)
	}
	ladder, err := Recover(stream)
	if err != nil {
		t.Fatal(err)
	}
	return ladder
}

func TestRecoverAllRenditions(t *testing.T) {
	ladder := buildLadder(t, fixture.CaseClean, []int{1_200_000, 2_400_000, 4_800_000})
	if len(ladder.Streams()) != 3 {
		t.Fatalf("renditions=%d want 3", len(ladder.Streams()))
	}
	for i, res := range ladder.Streams() {
		if len(res.Samples) == 0 {
			t.Fatalf("rendition %d recovered no samples", i)
		}
		for j := 1; j < len(res.Samples); j++ {
			if res.Samples[j].MediaNS <= res.Samples[j-1].MediaNS {
				t.Fatalf("rendition %d media time not monotonic", i)
			}
		}
	}
}

func TestDriftRecoveredFromSenderReports(t *testing.T) {
	ladder := buildLadder(t, fixture.CaseClean, []int{1_200_000, 2_400_000})
	for _, res := range ladder.Streams() {
		if d := res.DriftPPM - 250; d > 5 || d < -5 {
			t.Fatalf("rendition %d drift=%.2f ppm want 250±5", res.Rendition, res.DriftPPM)
		}
	}
}

func TestWrapScenarioUnwrapsMonotonically(t *testing.T) {
	ladder := buildLadder(t, fixture.CaseWrap, []int{1_200_000, 2_400_000})
	for _, res := range ladder.Streams() {
		if res.Wraps < 1 {
			t.Fatalf("rendition %d detected no wraps", res.Rendition)
		}
	}
	if pts := ladder.SwitchPoints(); len(pts) == 0 {
		t.Fatal("no switch points after wrap recovery")
	}
}

func TestLossScenarioCounters(t *testing.T) {
	ladder := buildLadder(t, fixture.CaseLoss, []int{1_200_000, 2_400_000})
	totalLost := uint64(0)
	for _, res := range ladder.Streams() {
		totalLost += res.Reorder.Lost
		if res.Reorder.PacketsIn == 0 {
			t.Fatal("no packets ingested")
		}
	}
	if totalLost == 0 {
		t.Fatal("loss scenario recorded zero committed losses")
	}
}

func TestCorruptScenarioRejectsBoundaries(t *testing.T) {
	ladder := buildLadder(t, fixture.CaseCorrupt, []int{1_200_000, 2_400_000})
	isolated := 0
	for _, res := range ladder.Streams() {
		isolated += int(res.Counters.Isolated)
	}
	if isolated == 0 {
		t.Fatal("corrupt samples were not isolated")
	}
}

func TestSwitchPointStructure(t *testing.T) {
	ladder := buildLadder(t, fixture.CaseClean, []int{1_200_000, 2_400_000, 4_800_000})
	points := ladder.SwitchPoints()
	if len(points) == 0 {
		t.Fatal("no aligned switch points")
	}
	for _, p := range points {
		if len(p.PerRendition) != 3 {
			t.Fatalf("switch point missing renditions: %+v", p)
		}
		if p.MaxSkewNS < 0 {
			t.Fatal("negative skew reported")
		}
		if p.Available && p.RejectedCause != clock.ErrNone {
			t.Fatal("available point carries a rejection cause")
		}
	}
}

func TestAlignmentThresholdEnforced(t *testing.T) {
	sp := SwitchPoint{MaxSkewNS: AlignmentThresholdNS + 1}
	if sp.MaxSkewNS <= AlignmentThresholdNS {
		t.Fatal("threshold test setup wrong")
	}
	// Direct constructor-level check through align(): build a tiny ladder
	// using two renditions with deliberately far keyframes.
	cfg, _ := fixture.DefaultConfig(7, 1_000_000_000, []int{1_000_000, 2_000_000})
	cfg.KeyframeOffset = []int{0, 7} // half-GOP phase skew
	stream, err := fixture.Generate(cfg, fixture.CaseClean)
	if err != nil {
		t.Fatal(err)
	}
	ladder, err := Recover(stream)
	if err != nil {
		t.Fatal(err)
	}
	if ladder.RejectedBoundaries() == 0 {
		t.Fatal("skewed keyframes produced no rejected switch point")
	}
}
