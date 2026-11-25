package segment

import (
	"testing"

	"mediasync/internal/model"
)

func seg(rep string, period, index int, start, dur int64, kf bool, kind model.AssetKind) model.SegmentDescriptor {
	return model.SegmentDescriptor{
		Source: "dash", Representation: rep, PeriodID: period, Index: index,
		Kind: kind, Keyframe: kf, StartNS: start, DurationNS: dur,
		Timescale: 1000, StartInScale: start / 1_000_000, DurationScale: dur / 1_000_000,
	}
}

func TestValidateOrderingContiguous(t *testing.T) {
	segs := []model.SegmentDescriptor{
		seg("v", 0, 0, 0, 1_000_000, true, model.KindVideo),
		seg("v", 0, 1, 1_000_000, 1_000_000, true, model.KindVideo),
		seg("v", 0, 2, 2_000_000, 1_000_000, true, model.KindVideo),
	}
	rep := ValidateOrdering(segs)
	if rep.Checked != 3 || len(rep.Errors) != 0 {
		t.Fatalf("contiguous stream must pass: %+v", rep.Errors)
	}
}

func TestValidateOrderingGapAndOverlap(t *testing.T) {
	segs := []model.SegmentDescriptor{
		seg("v", 0, 0, 0, 1_000_000, true, model.KindVideo),
		seg("v", 0, 1, 2_500_000, 1_000_000, true, model.KindVideo), // 500k gap
		seg("v", 0, 2, 3_000_000, 1_000_000, true, model.KindVideo), // 500k overlap
	}
	rep := ValidateOrdering(segs)
	if len(rep.Errors) != 2 {
		t.Fatalf("want 2 boundary errors, got %v", rep.Errors)
	}
	if model.Classify(rep.Errors[0]) != model.KindBoundaryGap || model.Locate(rep.Errors[0]) != 1 {
		t.Fatalf("first error must be located gap at index 1: %v", rep.Errors[0])
	}
	if model.Classify(rep.Errors[1]) != model.KindBoundaryOverlap || model.Locate(rep.Errors[1]) != 2 {
		t.Fatalf("second error must be located overlap at index 2: %v", rep.Errors[1])
	}
}

func TestPeriodTransitionIsNotABoundaryError(t *testing.T) {
	// HLS-style discontinuity: period 1 restarts at zero while period 0 ended
	// at 3ms. This must never be reported as a gap.
	segs := []model.SegmentDescriptor{
		seg("v", 0, 0, 0, 1_000_000, true, model.KindVideo),
		seg("v", 0, 1, 1_000_000, 2_000_000, true, model.KindVideo),
		seg("v", 1, 0, 0, 1_000_000, true, model.KindVideo),
	}
	if errs := ValidateOrdering(segs).Errors; len(errs) != 0 {
		t.Fatalf("period restart flagged incorrectly: %v", errs)
	}
}

func TestValidateKeyframes(t *testing.T) {
	segs := []model.SegmentDescriptor{
		seg("v", 0, 0, 0, 1_000_000, true, model.KindVideo),  // period first, exempt
		seg("v", 0, 1, 1_000_000, 1_000_000, false, model.KindVideo), // bad cut
		seg("v", 0, 2, 2_000_000, 1_000_000, true, model.KindVideo),
		seg("v", 1, 0, 0, 1_000_000, false, model.KindVideo), // first of new period, exempt
		seg("sub", 0, 0, 0, 1_000_000, false, model.KindSubtitle), // non-video, skipped
	}
	rep := ValidateKeyframes(segs)
	if len(rep.Errors) != 1 {
		t.Fatalf("want exactly one keyframe error, got %v", rep.Errors)
	}
	if model.Classify(rep.Errors[0]) != model.KindMissingKeyframe ||
		model.Locate(rep.Errors[0]) != 1 {
		t.Fatalf("error must locate segment index 1: %v", rep.Errors[0])
	}
}

func TestValidateAllGroupsByRepresentation(t *testing.T) {
	segs := []model.SegmentDescriptor{
		seg("video-hi", 0, 0, 0, 1_000_000, true, model.KindVideo),
		seg("video-hi", 0, 1, 2_000_000, 1_000_000, true, model.KindVideo), // gap
		seg("dialogue", 0, 0, 0, 1_000_000, false, model.KindDialogue),
		seg("dialogue", 0, 1, 1_000_000, 1_000_000, false, model.KindDialogue),
	}
	reports := ValidateAll(segs)
	if len(reports) != 2 {
		t.Fatalf("want one report per representation, got %d", len(reports))
	}
	byName := map[string]BoundaryReport{}
	for _, r := range reports {
		byName[r.Representation] = r
	}
	if len(byName["video-hi"].Errors) != 1 || len(byName["dialogue"].Errors) != 0 {
		t.Fatalf("group isolation broken: %+v", reports)
	}
}
