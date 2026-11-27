package pipeline_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/demux"
	"github.com/zld/continuity/internal/index"
	"github.com/zld/continuity/internal/mux"
	"github.com/zld/continuity/internal/pipeline"
)

func TestRunWithResumeCompletesAndMatchesFullRun(t *testing.T) {
	path, fp := fixture(t, 256, 4, mux.FaultNone)

	// Reference: one uninterrupted run into a separate checkpoint directory.
	refIn, err := demux.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	refStore, _ := index.NewStore(t.TempDir()+"/ref", fp)
	refIx, refMetrics, err := pipeline.Run(context.Background(), refIn, clock.DefaultPolicy(), refStore,
		pipeline.Config{ChunkSize: 64})
	refIn.Close()
	if err != nil {
		t.Fatalf("reference run: %v", err)
	}

	// Interrupted run: stop after two blocks.
	cpDir := t.TempDir() + "/cp"
	in1, _ := demux.Open(path)
	_, err = pipeline.RunWithResume(context.Background(), in1, clock.DefaultPolicy(), cpDir, fp,
		pipeline.Config{ChunkSize: 64, InterruptAfter: 2})
	in1.Close()
	if !errors.Is(err, clock.ErrInterrupted) {
		t.Fatalf("first run: want interrupt, got %v", err)
	}

	// Continuation: same parameters without the interrupt.
	in2, _ := demux.Open(path)
	out, err := pipeline.RunWithResume(context.Background(), in2, clock.DefaultPolicy(), cpDir, fp,
		pipeline.Config{ChunkSize: 64})
	in2.Close()
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if out.ResumedBlocks <= 0 {
		t.Fatalf("resumed blocks = %d, want > 0", out.ResumedBlocks)
	}

	// The resumed index summary must equal the single full run.
	if out.Index.EntryCount() != refIx.EntryCount() {
		t.Fatalf("entries resumed=%d full=%d", out.Index.EntryCount(), refIx.EntryCount())
	}
	if out.Index.SyncCount() != refIx.SyncCount() {
		t.Fatalf("sync counts differ: %d vs %d", out.Index.SyncCount(), refIx.SyncCount())
	}
	if out.Metrics.EntriesIndexed != refMetrics.EntriesIndexed {
		t.Fatalf("indexed entries resumed=%d full=%d",
			out.Metrics.EntriesIndexed, refMetrics.EntriesIndexed)
	}
}

func TestRunWithResumeRejectsChangedInput(t *testing.T) {
	path, fp := fixture(t, 128, 4, mux.FaultNone)
	cpDir := t.TempDir() + "/cp"
	in1, _ := demux.Open(path)
	_, err := pipeline.RunWithResume(context.Background(), in1, clock.DefaultPolicy(), cpDir, fp,
		pipeline.Config{ChunkSize: 64, InterruptAfter: 1})
	in1.Close()
	if !errors.Is(err, clock.ErrInterrupted) {
		t.Fatalf("first run: %v", err)
	}
	changed := fp
	changed[0] ^= 0xFF
	in2, _ := demux.Open(path)
	defer in2.Close()
	_, err = pipeline.RunWithResume(context.Background(), in2, clock.DefaultPolicy(), cpDir, changed,
		pipeline.Config{ChunkSize: 64})
	if !errors.Is(err, clock.ErrResumeMismatch) {
		t.Fatalf("changed input: want resume mismatch, got %v", err)
	}
}
