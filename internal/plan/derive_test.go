package plan_test

import (
	"errors"
	"testing"

	"mediad/internal/media"
	"mediad/internal/plan"
)

const second = media.NanoPerSecond

func demoCandidates() []plan.Candidate {
	return []plan.Candidate{
		{Width: 1920, Height: 1080, Bitrate: 4_000_000, Quality: plan.Rational{60, 100}, CostPerNs: 2},
		{Width: 1280, Height: 720, Bitrate: 2_500_000, Quality: plan.Rational{40, 100}, CostPerNs: 1},
		{Width: 960, Height: 540, Bitrate: 1_500_000, Quality: plan.Rational{25, 100}, CostPerNs: 1},
		{Width: 640, Height: 360, Bitrate: 800_000, Quality: plan.Rational{12, 100}, CostPerNs: 1},
	}
}

func TestDeriveSelects540pUnderCap(t *testing.T) {
	cons := plan.Constraints{
		MinQuality:  plan.Rational{20, 100},
		MaxBitrate:  1_500_000,
		BudgetUnits: 900,
	}
	dec, err := plan.Derive(demoCandidates(), cons, 600*second)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Width != 960 || dec.Height != 540 {
		t.Fatalf("selected %dx%d, want 960x540", dec.Width, dec.Height)
	}
	if dec.Bitrate != 1_500_000 {
		t.Fatalf("bitrate = %d", dec.Bitrate)
	}
	if plan.QualityFloat(dec.Quality) != 0.25 {
		t.Fatalf("quality = %v", dec.Quality)
	}
	if dec.BudgetUsed != 600 {
		t.Fatalf("budget used = %d, want 600 units (600 s)", dec.BudgetUsed)
	}
	if dec.Coverage.Num != 1 || dec.Coverage.Den != 1 {
		t.Fatalf("coverage = %v, want 1/1", dec.Coverage)
	}
	// 1080p and 720p fail the bitrate cap; 360p fails the quality floor.
	if len(dec.Rejections) != 3 {
		t.Fatalf("rejections = %d, want 3: %+v", len(dec.Rejections), dec.Rejections)
	}
}

func TestDeriveRejectionsAreExplicit(t *testing.T) {
	cons := plan.Constraints{
		MinQuality:  plan.Rational{50, 100},
		MaxBitrate:  1_000_000, // every candidate exceeds this
		BudgetUnits: 1 << 40,
	}
	_, err := plan.Derive(demoCandidates(), cons, 10*second)
	var nfc *plan.NoFeasibleCandidate
	if !errors.As(err, &nfc) {
		t.Fatalf("want NoFeasibleCandidate, got %v", err)
	}
	if len(nfc.Rejections) != 4 {
		t.Fatalf("rejections = %d, want 4", len(nfc.Rejections))
	}
}

func TestDeriveBudgetDrivesPartialCoverage(t *testing.T) {
	// With a generous bitrate cap the highest-quality 1080p candidate wins,
	// but a small budget cannot cover its full processing cost.
	cons := plan.Constraints{
		MinQuality:  plan.Rational{1, 100},
		MaxBitrate:  5_000_000,
		BudgetUnits: 300, // 1080p needs 1200 units for 600 s
	}
	dec, err := plan.Derive(demoCandidates(), cons, 600*second)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Height != 1080 {
		t.Fatalf("selected %dp, want 1080", dec.Height)
	}
	if dec.BudgetUsed != 1200 {
		t.Fatalf("budget used = %d, want 1200 units", dec.BudgetUsed)
	}
	if dec.Coverage.Num == 1 && dec.Coverage.Den == 1 {
		t.Fatalf("expected partial coverage, got %v", dec.Coverage)
	}
	if dec.Coverage.Num != 300 || dec.Coverage.Den != 1200 {
		t.Fatalf("coverage = %v, want 300/1200", dec.Coverage)
	}

	// Raising the budget above the winner's cost restores full coverage.
	full := cons
	full.BudgetUnits = 1200
	dec2, err := plan.Derive(demoCandidates(), full, 600*second)
	if err != nil {
		t.Fatal(err)
	}
	if dec2.Coverage.Num != 1 || dec2.Coverage.Den != 1 {
		t.Fatalf("coverage = %v, want 1/1", dec2.Coverage)
	}
}

func TestDeriveQualityFloorRejectsLowQuality(t *testing.T) {
	cons := plan.Constraints{
		MinQuality:  plan.Rational{30, 100},
		MaxBitrate:  1_500_000,
		BudgetUnits: 1 << 40,
	}
	_, err := plan.Derive(demoCandidates(), cons, 60*second)
	var nfc *plan.NoFeasibleCandidate
	if !errors.As(err, &nfc) {
		t.Fatalf("540p/360p fail quality and 720p/1080p fail bitrate: %v", err)
	}
}

func TestDeriveRejectsNonPositiveDuration(t *testing.T) {
	if _, err := plan.Derive(demoCandidates(), plan.Constraints{
		MinQuality: plan.Rational{1, 100}, MaxBitrate: 1 << 40, BudgetUnits: 1 << 40,
	}, 0); err == nil {
		t.Fatal("zero duration must be rejected")
	}
}

func TestDeriveTieBreakPrefersLowerBitrate(t *testing.T) {
	cands := []plan.Candidate{
		{Width: 100, Height: 100, Bitrate: 900, Quality: plan.Rational{50, 100}, CostPerNs: 1},
		{Width: 200, Height: 200, Bitrate: 700, Quality: plan.Rational{50, 100}, CostPerNs: 1},
	}
	dec, err := plan.Derive(cands, plan.Constraints{
		MinQuality: plan.Rational{10, 100}, MaxBitrate: 2000, BudgetUnits: 1 << 40,
	}, 10*second)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Bitrate != 700 {
		t.Fatalf("tie break selected bitrate %d, want 700", dec.Bitrate)
	}
}
