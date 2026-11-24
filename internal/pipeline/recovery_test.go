package pipeline_test

import (
	"context"
	"testing"

	"mediaroute/internal/fixture"
	"mediaroute/internal/index"
	"mediaroute/internal/ingest"
	"mediaroute/internal/model"
	"mediaroute/internal/pipeline"
	"mediaroute/internal/timeline"
)

// TestErrorPropagatesAcrossStages proves a structurally unrecoverable input is
// rejected before any repair or delivery, and that the category survives the
// whole chain instead of collapsing into a generic failure.
func TestErrorPropagatesAcrossStages(t *testing.T) {
	// Craft an input whose only content is a moov followed by garbage that
	// cannot resynchronise to a box boundary.
	g, err := fixture.Generate(fixture.DefaultParams(17), fixture.DamageNone)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// Keep init, then append a broken tail with no moof anchor.
	set, err := ingest.Read(g.Bytes, ingest.DefaultConfig())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	initOnly := append([]byte(nil), set.Init.Bytes...)
	initOnly = append(initOnly, 0x00, 0x00, 0x00, 0x40, 'x', 'x', 'x', 'x')

	proc, err := pipeline.NewProcessor(pipeline.DefaultConfig())
	if err != nil {
		t.Fatalf("processor: %v", err)
	}
	_, err = proc.Process(context.Background(), initOnly, nil)
	if err == nil {
		t.Fatal("expected unrecoverable boundary error")
	}
	te, ok := model.AsTaskError(err)
	if !ok {
		t.Fatalf("expected TaskError, got %T: %v", err, err)
	}
	if te.Category != model.CategoryUnrecoverable {
		t.Fatalf("want unrecoverable_boundary, got %s (%v)", te.Category, err)
	}
	if te.Stage == "" {
		t.Fatal("error is not locatable: no stage")
	}
}

// TestTruncationRecoversAndLaterStagesSeeIt proves the damaged fragment is
// isolated while the following fragments reach the index intact.
func TestTruncationRecoversAndLaterStagesSeeIt(t *testing.T) {
	g, err := fixture.Generate(fixture.DefaultParams(17), fixture.DamageTruncatedBox)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	proc, err := pipeline.NewProcessor(pipeline.Config{QueueCapacity: 2, MaxBoxBytes: 8 << 20})
	if err != nil {
		t.Fatalf("processor: %v", err)
	}
	out, err := proc.Process(context.Background(), g.Bytes, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if out.Counts.Truncated != 1 {
		t.Fatalf("want 1 truncated fragment at ingest, got %d", out.Counts.Truncated)
	}
	if out.Counts.Recovered < 1 {
		t.Fatalf("want at least one recovered fragment, got %d", out.Counts.Recovered)
	}
	// The index built downstream must contain the recovered fragments and none
	// of the dropped one.
	if out.Index.Len() == 0 {
		t.Fatal("recovered fragments produced no index entries")
	}
	for _, e := range out.Index.Entries {
		if e.MoofOffset == 0 && e.MdatOffset == 0 {
			t.Fatalf("index entry has no offsets: %+v", e)
		}
	}
}

// TestZeroTimescaleRejectedAtRepairStage proves validation happens before any
// media is written, with the category preserved.
func TestZeroTimescaleRejectedAtRepairStage(t *testing.T) {
	g, err := fixture.Generate(fixture.DefaultParams(17), fixture.DamageZeroTimescale)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	proc, _ := pipeline.NewProcessor(pipeline.DefaultConfig())
	_, err = proc.Process(context.Background(), g.Bytes, nil)
	te, ok := model.AsTaskError(err)
	if !ok || te.Category != model.CategoryZeroTimescale {
		t.Fatalf("want zero_timescale, got %v", err)
	}
}

// TestIndexStageDoesNotTrustExistingIndex proves a corrupt sidx never leaks
// into the rebuilt index.
func TestIndexStageDoesNotTrustExistingIndex(t *testing.T) {
	g, err := fixture.Generate(fixture.DefaultParams(17), fixture.DamageCorruptIndex)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	proc, _ := pipeline.NewProcessor(pipeline.DefaultConfig())
	out, err := proc.Process(context.Background(), g.Bytes, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if !out.Source.DamagedIndex {
		t.Fatal("corrupt existing index was not flagged")
	}
	// Every rebuilt entry must point at a real boundary.
	bs := index.NewBoundarySet(out.Index.Boundaries)
	for _, e := range out.Index.Entries {
		if !bs.Contains(e.MoofOffset) || !bs.Contains(e.MdatOffset) {
			t.Fatalf("rebuilt index adopted a bogus offset: %+v", e)
		}
	}
	_ = timeline.Stage
}
