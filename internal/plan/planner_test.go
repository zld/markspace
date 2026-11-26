package plan_test

import (
	"context"
	"testing"

	"regress/internal/fault"
	"regress/internal/plan"
	"regress/internal/scan"
)

// fakeEncoder returns fully deterministic, in-memory trial outcomes so the
// planner's selection and rejection logic is tested without FFmpeg. SSIM is
// served per evaluation call in ladder order.
type fakeEncoder struct {
	ssim     []float64
	frames   int
	segments int
	idx      int
}

func (f *fakeEncoder) Encode(_ context.Context, _ string, c plan.Candidate) (*plan.EncoderOutput, error) {
	perSeg := int64(80_000 - c.ID*12_000)
	if perSeg < 4_000 {
		perSeg = 4_000
	}
	out := &plan.EncoderOutput{FrameCount: f.frames, Path: "memory://fake"}
	for i := 0; i < f.segments; i++ {
		out.SegmentBytes = append(out.SegmentBytes, perSeg)
		out.OutputBytes += perSeg
	}
	return out, nil
}

func (f *fakeEncoder) MeasureSSIM(context.Context, string, *plan.EncoderOutput) (float64, error) {
	v := f.ssim[f.idx]
	f.idx++
	return v, nil
}

func syntheticScan(keyframes, total int) *scan.ScanResult {
	res := &scan.ScanResult{Codec: scan.CodecH264}
	kf := makeRange(keyframes, total)
	kfSet := map[int]bool{}
	for _, k := range kf {
		kfSet[k] = true
	}
	for i := 0; i < total; i++ {
		res.AccessUnits = append(res.AccessUnits, scan.AccessUnit{Index: i, IsRAP: kfSet[i]})
	}
	res.Keyframes = kf
	res.StreamBytes = 1000
	return res
}

func makeRange(keyframes, total int) []int {
	var out []int
	step := total / keyframes
	for k := 0; k < keyframes; k++ {
		out = append(out, k*step)
	}
	return out
}

func TestPlannerSelectsFirstFeasible(t *testing.T) {
	res := syntheticScan(4, 48)
	enc := &fakeEncoder{
		ssim:     []float64{0.99, 0.98, 0.97, 0.96, 0.95},
		frames:   48,
		segments: 4,
	}
	con := plan.Constraints{SSIMMin: 0.93, MaxBitrateBPS: 600_000, WorkBudget: 200_000, Timescale: 24}
	dec, err := plan.NewPlanner(enc, scan.CodecH264, res, con).Run(context.Background())
	if err != nil {
		t.Fatalf("expected a selection, got %v", err)
	}
	if dec.Selected == nil {
		t.Fatal("no candidate selected")
	}
	if dec.ConfigHash == "" {
		t.Fatal("config hash not populated")
	}
	if len(dec.Results) != 5 {
		t.Fatalf("evaluated %d rungs, want 5", len(dec.Results))
	}
}

func TestPlannerUnsatisfiableBudget(t *testing.T) {
	res := syntheticScan(4, 48)
	enc := &fakeEncoder{
		ssim:     []float64{0.99, 0.98, 0.97, 0.96, 0.95},
		frames:   48,
		segments: 4,
	}
	// An impossibly small work budget rejects every rung.
	con := plan.Constraints{SSIMMin: 0.93, MaxBitrateBPS: 600_000, WorkBudget: 1, Timescale: 24}
	dec, err := plan.NewPlanner(enc, scan.CodecH264, res, con).Run(context.Background())
	if err == nil {
		t.Fatal("expected budget-unsatisfiable error")
	}
	if !fault.HasCode(err, fault.CodeBudgetUnsatisfiable) {
		t.Fatalf("want %s, got %v", fault.CodeBudgetUnsatisfiable, fault.Codes(err))
	}
	if dec.Selected != nil {
		t.Fatal("must not select a candidate when the budget is unsatisfiable")
	}
	if len(dec.Results) != 5 {
		t.Fatalf("want all 5 rungs evaluated, got %d", len(dec.Results))
	}
	for _, r := range dec.Results {
		if r.Accepted || len(r.Rejects) == 0 {
			t.Fatalf("rung %d should carry a rejection", r.Candidate.ID)
		}
	}
}

func TestPlannerQualityFloorRejection(t *testing.T) {
	res := syntheticScan(4, 48)
	enc := &fakeEncoder{
		ssim:     []float64{0.80, 0.79, 0.78, 0.77, 0.76},
		frames:   48,
		segments: 4,
	}
	con := plan.Constraints{SSIMMin: 0.93, MaxBitrateBPS: 6_000_000, WorkBudget: 200_000, Timescale: 24}
	dec, err := plan.NewPlanner(enc, scan.CodecH264, res, con).Run(context.Background())
	if err == nil || dec.Selected != nil {
		t.Fatalf("expected no publish, err=%v", err)
	}
	if !fault.HasCode(err, fault.CodeQualityBelowMin) {
		t.Fatalf("want quality rejection codes, got %v", fault.Codes(err))
	}
}

func TestDeterministicWorkStable(t *testing.T) {
	c := plan.Candidate{ID: 2, WorkPerFrame: 250}
	a := plan.DeterministicWork(c, 48, 120_000)
	b := plan.DeterministicWork(c, 48, 120_000)
	if a != b || a <= 0 {
		t.Fatalf("work estimate unstable or non-positive: %d vs %d", a, b)
	}
}

func TestSegmentPlanUsesKeyframeTable(t *testing.T) {
	res := syntheticScan(4, 48)
	sp := plan.BuildSegmentPlan(res, 24)
	if len(sp.FramesPerSegment) != 4 {
		t.Fatalf("segments = %d, want 4", len(sp.FramesPerSegment))
	}
	total := 0
	for _, n := range sp.FramesPerSegment {
		total += n
	}
	if total != 48 {
		t.Fatalf("segment frames total %d, want 48", total)
	}
	if sp.KeyframeOrdinals[0] != 0 || sp.KeyframeOrdinals[1] != 12 {
		t.Fatalf("boundary ordinals not keyframe aligned: %v", sp.KeyframeOrdinals)
	}
}
