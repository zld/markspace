package batch_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"replaycheck/internal/batch"
	"replaycheck/internal/clock"
	"replaycheck/internal/fixture"
)

func TestRunSelfTestRecoveryAndDigest(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "nested", "index.json")
	report, err := batch.RunSelfTest(indexPath, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK() {
		for _, c := range report.Checks {
			if !c.OK {
				t.Errorf("failed check %s: %s", c.Name, c.Detail)
			}
		}
		t.Fatal("self-test report did not pass")
	}
	if report.Recovery.Metrics.WrapCount != 1 || report.Recovery.Metrics.Files != 5 {
		t.Fatalf("unexpected metrics: %+v", report.Recovery.Metrics)
	}
	if report.RecoveryDigest != report.RepeatDigest {
		t.Fatal("recovery and fresh index digests differ")
	}
}

func TestResumeRejectsChangedInputDigest(t *testing.T) {
	dir := t.TempDir()
	set, err := fixture.GenerateSet(filepath.Join(dir, "samples"))
	if err != nil {
		t.Fatal(err)
	}
	cpPath := filepath.Join(dir, "index.checkpoint.json")
	_, err = batch.Run(batch.Config{
		Samples: set.Samples, CheckpointPath: cpPath, ChunkEvery: 4,
		InterruptKey: "chunk_boundary_corruption",
	})
	if !errors.Is(err, batch.ErrInterrupted) {
		t.Fatalf("first run error = %v, want interruption", err)
	}

	changed := set.Samples[0].Path + ".changed"
	if err := os.WriteFile(set.Samples[0].Path, []byte("tampered input digest"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = batch.Run(batch.Config{
		Samples: set.Samples, CheckpointPath: cpPath, ChunkEvery: 4, Resume: true,
	})
	var ce *clock.Error
	if !errors.As(err, &ce) || ce.Code != clock.CodeDigestMismatch {
		t.Fatalf("resume error = %v, want digest mismatch", err)
	}
	_ = changed
}

func TestFailedInputsProduceExitCodeOne(t *testing.T) {
	dir := t.TempDir()
	set, err := fixture.GenerateSet(filepath.Join(dir, "samples"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := batch.Run(batch.Config{
		Samples: set.Samples, ChunkEvery: 4,
		IndexPath: filepath.Join(dir, "index.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 1 {
		t.Fatalf("exit code = %d, want 1 for unrecoverable fixtures", res.ExitCode)
	}
	if res.IndexSHA256 == "" {
		t.Fatal("batch failure index should still persist item conclusions")
	}
}

func TestBusinessThresholdViolationProducesExitCodeTwo(t *testing.T) {
	dir := t.TempDir()
	set, err := fixture.GenerateSet(filepath.Join(dir, "samples"))
	if err != nil {
		t.Fatal(err)
	}
	normal := set.Samples[:1]
	res, err := batch.Run(batch.Config{
		Samples: normal, ChunkEvery: 4,
		IndexPath: filepath.Join(dir, "index.json"),
		// Any nonnegative deviation exceeds a negative test threshold.
		BudgetDeviationThresholdPct: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 2 {
		t.Fatalf("exit code = %d, want 2", res.ExitCode)
	}
}
