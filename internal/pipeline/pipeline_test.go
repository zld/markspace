package pipeline

import (
	"errors"
	"os"
	"testing"

	"github.com/local/medialab/internal/container"
	"github.com/local/medialab/internal/fixture"
	"github.com/local/medialab/internal/model"
	"github.com/local/medialab/internal/stream"
	"github.com/local/medialab/internal/txn"
)

func baseConfig(t *testing.T, interrupt string) Config {
	t.Helper()
	return Config{
		Seed: 7, LossBurst: 1, LossAt: 4, Reorder: 3, Duplicate: 2,
		Bandwidth:   []int{800, 300, 900},
		InterruptAt: interrupt,
		WorkDir:     t.TempDir(),
	}
}

func TestRunHealthyPipelineMeetsAssertions(t *testing.T) {
	res, err := Run(baseConfig(t, ""))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	s := res.Summary
	if s.AVSyncMs > 20 {
		t.Errorf("av_sync_ms = %d, want <= 20", s.AVSyncMs)
	}
	if s.RecoveryRate < 0.75 {
		t.Errorf("recovery_rate = %v, want >= 0.75", s.RecoveryRate)
	}
	if s.P95LatencyMs > 500 {
		t.Errorf("p95_latency_ms = %d, want <= 500", s.P95LatencyMs)
	}
	if s.KeyframeGapMs != 500 {
		t.Errorf("keyframe_gap_ms = %d, want 500", s.KeyframeGapMs)
	}
	// MXF primaries 6 vs MP4 primaries 1 -> exactly one colour conflict.
	if s.ConflictCount != 1 {
		t.Errorf("conflict_count = %d, want 1", s.ConflictCount)
	}
	if !s.Healthy {
		t.Error("clean run must be healthy")
	}
	if res.ContentHash == "" {
		t.Fatal("content hash missing")
	}
	if s.CommitID != StageReport {
		t.Errorf("commit id = %q, want %q", s.CommitID, StageReport)
	}
}

func TestRunIdempotentAcrossRepeatedRuns(t *testing.T) {
	cfg := baseConfig(t, "")
	first, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(cfg)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if first.ContentHash != second.ContentHash {
		t.Fatalf("content hash drifted:\n %s\n %s", first.ContentHash, second.ContentHash)
	}
	if first.Summary.CommitID != second.Summary.CommitID {
		t.Fatalf("commit id changed: %q vs %q", first.Summary.CommitID, second.Summary.CommitID)
	}
}

func TestRunInterruptAndResume(t *testing.T) {
	cfg := baseConfig(t, "commit-1")
	_, err := Run(cfg)
	if !errors.Is(err, txn.ErrInterrupted) {
		t.Fatalf("want ErrInterrupted, got %v", err)
	}
	// Journal on disk must retain exactly the first boundary.
	j, err := txn.OpenJournal(cfg.WorkDir + "/txn")
	if err != nil {
		t.Fatal(err)
	}
	if !j.Committed(StageContainer) {
		t.Fatal("commit-0 boundary must survive the interruption")
	}
	if j.Committed(StageStream) {
		t.Fatal("commit-1 must not be durable after pre-commit crash")
	}
	_ = j.Close()

	// Resume without the fault: remaining stages run, hash matches a clean run.
	resumeCfg := cfg
	resumeCfg.InterruptAt = ""
	resumed, err := Run(resumeCfg)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	cleanCfg := cfg
	cleanCfg.InterruptAt = ""
	cleanCfg.WorkDir = t.TempDir()
	clean, err := Run(cleanCfg)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ContentHash != clean.ContentHash {
		t.Fatalf("resumed hash %s != clean hash %s", resumed.ContentHash, clean.ContentHash)
	}
	if resumed.Summary.CommitID != StageReport {
		t.Fatalf("final commit = %q, want %q", resumed.Summary.CommitID, StageReport)
	}
}

func TestRunDamagedSampleReportsRanges(t *testing.T) {
	// A damaged MP4 is produced by the fixture and must be reflected in
	// located damage ranges. The pipeline's own container stage uses a
	// clean file, so drive the damaged path through the stream subsystem
	// directly and confirm the book records truncation windows.
	clean, _ := fixture.GenerateMP4(fixture.DefaultMP4Spec(7))
	damaged := fixture.CorruptMP4(clean, fixture.CorruptTruncation)
	if len(damaged) >= len(clean) {
		t.Fatal("corruption did not shorten the file")
	}
	// Stream analysis against a short read that reaches into the video
	// chunk must surface incomplete video frames.
	meta, err := container.ParseMP4(clean)
	if err != nil {
		t.Fatal(err)
	}
	shortSize := int64(len(damaged)) - 80*180 - 120
	book := &model.ErrorBook{}
	stats := stream.AnalyzeFrames(meta.Samples, shortSize, book)
	if stats.IncompleteCount == 0 {
		t.Fatal("truncated read must produce incomplete frames")
	}
	if !book.Has(model.CodeTruncated) && !book.Has(model.CodeIncompleteFrame) {
		t.Fatal("no located truncation damage recorded")
	}
}

func TestWorkDirArtifactsExist(t *testing.T) {
	cfg := baseConfig(t, "")
	if _, err := Run(cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"txn/journal.wal", "txn/committed.id"} {
		if _, err := os.Stat(cfg.WorkDir + "/" + name); err != nil {
			t.Errorf("artifact %s missing: %v", name, err)
		}
	}
}
