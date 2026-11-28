package policy

import (
	"testing"

	merr "mediacheck/internal/errors"
)

func healthyRequest() Request {
	return Request{
		Quality: QualityObservation{
			PixelMean: 100, ReferenceMean: 100,
			PixelStddev: 40, ReferenceStddev: 40,
			AudioPeakDBFS: -12.5,
		},
		Threshold: DefaultThresholds(),
		ElapsedMS: 120,
		BudgetMS:  5000,
		MaxBytes:  2_000_000,
		Ladder: []Representation{
			{LayerID: 0, Label: "high", Bitrate: 2_000_000, EstimatedSize: 1_800_000},
			{LayerID: 1, Label: "mid", Bitrate: 1_200_000, EstimatedSize: 1_100_000},
			{LayerID: 2, Label: "low", Bitrate: 600_000, EstimatedSize: 600_000},
		},
	}
}

func TestAcceptsHighestFittingRepresentation(t *testing.T) {
	out := Evaluate(healthyRequest())
	if out.Decision != DecisionAccepted || out.Selected == nil || out.Selected.Label != "high" {
		t.Fatalf("decision = %+v; want accepted/high", out)
	}
}

func TestSizeGatePicksLowerRungWithoutQualityLoss(t *testing.T) {
	req := healthyRequest()
	req.MaxBytes = 1_500_000 // high no longer fits; mid must be chosen
	out := Evaluate(req)
	if out.Decision != DecisionAccepted || out.Selected.Label != "mid" {
		t.Fatalf("decision = %+v; want accepted/mid", out)
	}
}

func TestSizeGateRejectsWhenNothingFits(t *testing.T) {
	req := healthyRequest()
	req.MaxBytes = 10
	out := Evaluate(req)
	if out.Decision != DecisionSizeRejected || out.ErrorCode != merr.CodeSizeLimit {
		t.Fatalf("decision = %+v; want size_rejected", out)
	}
}

func TestQualityGateRunsFirstAndBlocksEvenIfSizeFits(t *testing.T) {
	req := healthyRequest()
	req.Quality.PixelMean = req.Quality.ReferenceMean + 5 // pixel deviation
	out := Evaluate(req)
	if out.Decision != DecisionQualityFailed || out.ErrorCode != merr.CodeQualityGate {
		t.Fatalf("decision = %+v; want quality_failed", out)
	}
	if out.Selected != nil {
		t.Fatal("a failed quality gate must never select a representation")
	}
}

func TestLoudnessFloorEnforced(t *testing.T) {
	req := healthyRequest()
	req.Quality.AudioPeakDBFS = -40 // below -24 dBFS floor
	out := Evaluate(req)
	if out.Decision != DecisionQualityFailed {
		t.Fatalf("decision = %+v; want quality_failed on loudness", out)
	}
}

func TestBudgetGateMarksIncompleteBeforeSizeGate(t *testing.T) {
	req := healthyRequest()
	req.ElapsedMS = 6000 // over the 5000ms budget, even though everything fits
	out := Evaluate(req)
	if out.Decision != DecisionIncomplete || out.ErrorCode != merr.CodeBudgetExceeded {
		t.Fatalf("decision = %+v; want incomplete", out)
	}
	if out.Selected != nil {
		t.Fatal("budget exhaustion must not silently deliver output")
	}
}

func TestQualityFailureOutranksBudgetFailure(t *testing.T) {
	req := healthyRequest()
	req.Quality.AudioPeakDBFS = -50
	req.ElapsedMS = 99999
	out := Evaluate(req)
	if out.Decision != DecisionQualityFailed {
		t.Fatalf("decision = %+v; quality gate must win", out)
	}
}

func TestZeroBudgetMeansUnlimited(t *testing.T) {
	req := healthyRequest()
	req.BudgetMS = 0
	req.ElapsedMS = 1 << 40
	out := Evaluate(req)
	if out.Decision != DecisionAccepted {
		t.Fatalf("zero budget should disable gate, got %+v", out)
	}
}
