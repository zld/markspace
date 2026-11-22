package pipeline_test

import (
	"errors"
	"testing"

	"example.com/mediatimeline/internal/fixture"
	"example.com/mediatimeline/internal/model"
	"example.com/mediatimeline/internal/pipeline"
)

func TestDeterminism(t *testing.T) {
	sc, faults := build(t, 42, fixture.FaultWrap, fixture.FaultLoss, fixture.FaultColorConflict)
	a, err := run(t, sc, faults)
	if err != nil {
		t.Fatal(err)
	}
	b, err := run(t, sc, faults)
	if err != nil {
		t.Fatal(err)
	}
	if a.SegmentHashMatch != b.SegmentHashMatch || a.RoundTripMatch != b.RoundTripMatch || a.KeyframeHitRate != b.KeyframeHitRate {
		t.Fatal("metrics differ between identical runs")
	}
	if len(a.SegmentIndexes) != len(b.SegmentIndexes) {
		t.Fatal("segment index length differs")
	}
	for i := range a.SegmentIndexes {
		if a.SegmentIndexes[i].Hash != b.SegmentIndexes[i].Hash {
			t.Fatalf("segment %d hash is not reproducible", i)
		}
	}
	if len(a.KeyframeIndex.Entries) != len(b.KeyframeIndex.Entries) {
		t.Fatal("keyframe count differs")
	}
	for i := range a.KeyframeIndex.Entries {
		if a.KeyframeIndex.Entries[i] != b.KeyframeIndex.Entries[i] {
			t.Fatalf("keyframe %d differs between runs", i)
		}
	}
	da := pipeline.FormatDecisions(a.Decisions)
	db := pipeline.FormatDecisions(b.Decisions)
	if len(da) != len(db) {
		t.Fatal("decision count differs between runs")
	}
	for i := range da {
		if da[i] != db[i] {
			t.Fatalf("decision %d differs:\n%s\n%s", i, da[i], db[i])
		}
	}
}

func TestLimits(t *testing.T) {
	sc, faults := build(t, 42)
	in := fixture.PipelineInput(sc, faults)
	rep, err := pipeline.Run(in, pipeline.Limits{MaxInputBytes: 8, MaxEventMemoryBytes: 1 << 30})
	if err == nil {
		t.Fatal("expected the input limit to fail the run")
	}
	if rep == nil {
		t.Fatal("report must be returned even on limit failure")
	}
	pe, ok := model.AsError(err)
	if !ok || pe.Code != model.CodeLimitExceeded {
		t.Fatalf("unexpected error %v", err)
	}
	if pipeline.ExitCode(err) != 1 {
		t.Fatalf("parameter/limit failures exit 1, got %d", pipeline.ExitCode(err))
	}
}

func TestEmptyInputFails(t *testing.T) {
	_, err := pipeline.Run(pipeline.Input{}, pipeline.DefaultLimits())
	if err == nil {
		t.Fatal("empty input must fail")
	}
	var pe *model.Error
	if !errors.As(err, &pe) {
		t.Fatalf("empty input error is not classified: %v", err)
	}
}
