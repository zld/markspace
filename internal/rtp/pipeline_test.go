package rtp

import (
	"testing"
	"time"

	"rtspdiag/internal/fixture"
)

func TestPipelineWithFixture(t *testing.T) {
	plan := fixture.DefaultPlan(5)
	items := fixture.Generate(42, plan)
	p := NewPipeline(1500, 8, 25*time.Millisecond)
	now := time.Unix(0, 0)

	for _, it := range items {
		// A malformed item must be counted, skipped and never panic.
		_ = p.Ingest(it.Raw, now)
	}
	p.Flush(now)
	st := p.Stats()

	if st.ParseErrors != 1 {
		t.Fatalf("parse errors = %d want 1", st.ParseErrors)
	}
	if st.GapEvents < 1 || st.MissingPackets < 1 {
		t.Fatalf("expected at least one gap: %+v", st)
	}
	if st.Duplicates < 1 {
		t.Fatalf("expected duplicate accounting: %+v", st)
	}
	if st.CompleteFrames != plan.Frames {
		t.Fatalf("complete frames = %d want %d", st.CompleteFrames, plan.Frames)
	}
}

func TestPipelineResetIsClean(t *testing.T) {
	items := fixture.Generate(42, fixture.DefaultPlan(3))
	p := NewPipeline(1500, 8, 25*time.Millisecond)
	now := time.Unix(0, 0)
	for _, it := range items {
		_ = p.Ingest(it.Raw, now)
	}
	p.Reset()
	st := p.Stats()
	if st != (PipelineStats{}) {
		t.Fatalf("reset left counters: %+v", st)
	}
	// Fresh ingest after reset works from an empty pipeline.
	_ = p.Ingest(items[0].Raw, now)
	if got := p.Stats(); got.Received != 1 || got.ParseErrors != 0 {
		t.Fatalf("post-reset ingest wrong: %+v", got)
	}
}
