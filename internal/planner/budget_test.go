package planner

import (
	"testing"

	"continuity/internal/model"
)

func baseConstraints(frames int) Constraints {
	return Constraints{
		Threads: 4, MaxOutputBytes: 1 << 30, LoudnessTolerance: 8.0,
		FrameCount: frames, FrameDurationNS: 20_000_000, SourceRate: 48000,
	}
}

func TestPlanSelectsFirstFeasibleCandidate(t *testing.T) {
	res, err := Plan(baseConstraints(10))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.Selected == nil {
		t.Fatal("no candidate selected")
	}
	// Enumeration order starts at 48 kHz, mono, -23 LUFS.
	want := Params{SampleRate: 48000, Channels: model.LayoutMono, TargetLUFS: -23.0}
	if *res.Selected != want {
		t.Fatalf("selected = %+v, want %+v", *res.Selected, want)
	}
}

func TestPlanRejectsWhenByteCeilingTooSmall(t *testing.T) {
	con := baseConstraints(10)
	con.MaxOutputBytes = 1 // every candidate exceeds one byte
	res, err := Plan(con)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.Selected != nil {
		t.Fatal("candidate selected despite impossible byte ceiling")
	}
	if len(res.Rejections) == 0 {
		t.Fatal("expected rejection reasons")
	}
	if err = res.MustHaveSelection(); err == nil {
		t.Fatal("MustHaveSelection should fail with no feasible candidate")
	}
}

func TestPlanToleranceZeroSelectsOnlyDeliveryAnchor(t *testing.T) {
	con := baseConstraints(10)
	// Tolerance zero means only a candidate exactly on the -23 LUFS delivery
	// anchor is admissible; the first feasible pick must therefore be it.
	con.LoudnessTolerance = 0
	res, err := Plan(con)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.Selected == nil || res.Selected.TargetLUFS != -23.0 {
		t.Fatalf("selected target = %+v, want -23 LUFS anchor", res.Selected)
	}
}

func TestPlanAllCandidatesRejectedWhenToleranceNegativeDomain(t *testing.T) {
	// A tolerance below the smallest anchor distance excludes every non-anchor
	// target and, when the byte ceiling also blocks the anchor, yields no plan.
	con := baseConstraints(10)
	con.LoudnessTolerance = 0
	con.MaxOutputBytes = 0
	res, err := Plan(con)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.Selected != nil {
		t.Fatal("expected no feasible candidate under combined constraints")
	}
	for _, r := range res.Rejections {
		if r.Reason == "" {
			t.Fatal("rejection missing reason")
		}
	}
}

func TestPlanValidatesInputs(t *testing.T) {
	if _, err := Plan(Constraints{Threads: 0, FrameCount: 1}); err == nil {
		t.Fatal("zero threads should be rejected")
	}
	if _, err := Plan(Constraints{Threads: 1, FrameCount: 0}); err == nil {
		t.Fatal("zero frames should be rejected")
	}
}

func TestEstimatedOutputBytes(t *testing.T) {
	con := baseConstraints(10)
	// 48 kHz * 0.02 s = 960 samples/frame; mono *2 bytes *10 frames = 19200.
	got := EstimatedOutputBytes(Params{SampleRate: 48000, Channels: model.LayoutMono}, con)
	if got != 19200 {
		t.Fatalf("mono estimate = %d, want 19200", got)
	}
	gotStereo := EstimatedOutputBytes(Params{SampleRate: 48000, Channels: model.LayoutStereo}, con)
	if gotStereo != 38400 {
		t.Fatalf("stereo estimate = %d, want 38400", gotStereo)
	}
}
