package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"archivist/internal/fault"
	"archivist/internal/fixture"
)

// TestWriteFailureKeepsResumableCheckpoint simulates an output directory that
// cannot be written by placing a file where the output directory should be.
// The write stage must fail, no output may be published, and the checkpoint
// must remain so a retry can continue.
func TestWriteFailureKeepsResumableCheckpoint(t *testing.T) {
	root, inputs := setup(t, 10)
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	// A regular file where the output directory belongs.
	blocker := filepath.Join(work, "output")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, root, 1, 0)
	results, err := r.Run(context.Background(), inputs)
	if err == nil {
		t.Fatal("expected write failure")
	}
	if fault.ExitCode(err) != 1 {
		t.Fatalf("exit = %d", fault.ExitCode(err))
	}
	res := results[0]
	if res.Status != "error" || res.OutputSHA256 != "" {
		t.Fatalf("result = %+v", res)
	}
	cp, err := loadCheckpoint(res.CheckpointPath)
	if err != nil || cp == nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if !cp.Has(StageScan) || !cp.Has(StageRepair) || cp.Has(StageWrite) {
		t.Fatalf("completed = %v", cp.Completed)
	}
	if cp.Status == "ok" {
		t.Fatal("failed run marked the checkpoint ok")
	}

	// Remove the obstruction: the retry must resume rather than restart.
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	r2 := newRunner(t, root, 1, 0)
	results2, err := r2.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if results2[0].Status != "ok" {
		t.Fatalf("retry status = %s (%s)", results2[0].Status, results2[0].Error)
	}
	for _, s := range results2[0].ExecutedStages {
		if s == StageScan || s == StageRepair {
			t.Fatalf("retry re-executed %s", s)
		}
	}
	if _, err := os.Stat(results2[0].OutputPath); err != nil {
		t.Fatalf("output missing after retry: %v", err)
	}
	// A leftover temporary file would mean the failed write leaked one.
	if _, err := os.Stat(results2[0].OutputPath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary file was left behind: %v", err)
	}
}

// TestMissingStageArtifactRerunsDependentStages deletes the repair artifact
// and confirms the pipeline rebuilds from that point without touching the
// still-valid scan artifact's stage.
func TestMissingStageArtifactRerunsDependentStages(t *testing.T) {
	root, inputs := setup(t, 8)
	r := newRunner(t, root, 1, 0)
	first, err := r.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := loadCheckpoint(first[0].CheckpointPath)
	if err != nil || cp == nil {
		t.Fatal(err)
	}
	if err := os.Remove(cp.Repair.Path); err != nil {
		t.Fatal(err)
	}
	second, err := r.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if second[0].OutputSHA256 != first[0].OutputSHA256 {
		t.Fatal("rebuilt output differs from the original")
	}
	var reran []Stage
	for _, s := range second[0].ExecutedStages {
		reran = append(reran, s)
	}
	if len(reran) == 0 {
		t.Fatal("nothing reran after the artifact was deleted")
	}
	for _, s := range reran {
		if s == StageScan {
			t.Fatal("scan reran even though its artifact was valid")
		}
	}
}

// TestQueueFullBlocksProducer verifies the producer cannot outrun the queue:
// with capacity 1 and inputs that each take measurable time, at most one
// input is in flight and every input still completes.
func TestQueueFullBlocksProducer(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "inputs")
	var inputs []string
	for i := 0; i < 5; i++ {
		opts := fixture.DefaultOptions(string(rune('a'+i)), fixture.KindNormal)
		opts.SampleCount = 200
		s, err := fixture.Build(dir, opts)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, s.Path)
	}
	r := newRunner(t, root, 1, 0)
	results, err := r.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("results = %d", len(results))
	}
	if r.MaxInFlight() != 1 {
		t.Fatalf("max in flight = %d with capacity 1", r.MaxInFlight())
	}
}
