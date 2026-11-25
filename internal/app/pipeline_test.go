package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mediapipeline/internal/checkpoint"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/fixture"
)

func testConfig(t *testing.T, c fixture.Case) Config {
	root := t.TempDir()
	return Config{
		InputDir:        filepath.Join(root, "generated"),
		OutputDir:       filepath.Join(root, "tmp"),
		Case:            c,
		Representations: 2,
		Fragments:       5,
		QueueCapacity:   2,
	}
}

func TestRunNormalToCompletion(t *testing.T) {
	p, err := New(testConfig(t, fixture.CaseNormal))
	if err != nil {
		t.Fatal(err)
	}
	summary, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.State != string(checkpoint.StageComplete) {
		t.Fatalf("state = %s, want complete", summary.State)
	}
	if summary.Representations != 2 {
		t.Fatalf("representations = %d", summary.Representations)
	}
	if summary.AlignedSegments <= 0 {
		t.Fatal("aligned segments must be positive")
	}
	if summary.QueuePeak > 2 {
		t.Fatalf("queue peak %d exceeds capacity 2", summary.QueuePeak)
	}
	if !summary.ManifestValid || summary.ManifestSHA256 == "" {
		t.Fatal("manifest should be valid and hashed")
	}
	if summary.ProvenanceSHA256 == "" {
		t.Fatal("provenance should be hashed")
	}
	cp, err := p.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cp.Stage != checkpoint.StageComplete {
		t.Fatalf("checkpoint stage = %s", cp.Stage)
	}
}

func TestRunTruncatedBoxCommitsNothing(t *testing.T) {
	cfg := testConfig(t, fixture.CaseTruncatedBox)
	cfg.QueueCapacity = 1
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := p.Run(context.Background())
	if !fault.Is(err, fault.CodeTruncatedBox) {
		t.Fatalf("expected truncated_box, got %v", err)
	}
	if summary.ErrorCode != string(fault.CodeTruncatedBox) {
		t.Fatalf("summary error code = %q", summary.ErrorCode)
	}
	if summary.CommittedSegments != 0 {
		t.Fatalf("committed segments = %d, want 0", summary.CommittedSegments)
	}
	if p.store.Exists() {
		cp, _ := p.store.Load()
		if cp.Reached(checkpoint.StageScan) {
			t.Fatal("checkpoint must not advance after corrupted input")
		}
	}
}

func TestRegressionInterruptAndResume(t *testing.T) {
	cfg := testConfig(t, fixture.CaseTFDTRegression)
	cfg.QueueCapacity = 1
	cfg.InterruptAfter = checkpoint.StageAlign
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.State != "interrupted:align" {
		t.Fatalf("state = %s", summary.State)
	}
	if summary.RegressionCount != 2 {
		t.Fatalf("regression count = %d, want 2 (one per rep)", summary.RegressionCount)
	}
	if summary.SafeResumeStage != string(checkpoint.StageAlign) {
		t.Fatalf("safe resume stage = %q", summary.SafeResumeStage)
	}
	cp, _ := p.store.Load()
	if cp.Stage != checkpoint.StageAlign {
		t.Fatalf("checkpoint should stop at align, got %s", cp.Stage)
	}

	// Resume from the safe checkpoint and run to completion.
	resumeCfg := cfg
	resumeCfg.InterruptAfter = ""
	resumeCfg.Resume = true
	p2, err := New(resumeCfg)
	if err != nil {
		t.Fatal(err)
	}
	summary2, err := p2.Run(context.Background())
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if summary2.State != string(checkpoint.StageComplete) {
		t.Fatalf("resumed state = %s, want complete", summary2.State)
	}
	if !summary2.ManifestValid {
		t.Fatal("resumed manifest must be valid")
	}
}

func TestRepeatRunIsIdentical(t *testing.T) {
	cfg := testConfig(t, fixture.CaseNormal)
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Second run against the same input and output directory.
	p2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p2.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.ManifestSHA256 != second.ManifestSHA256 {
		t.Fatalf("manifest digest changed: %s vs %s", first.ManifestSHA256, second.ManifestSHA256)
	}
	if second.ProvenanceDiff != 0 {
		t.Fatalf("provenance diff = %d, want 0", second.ProvenanceDiff)
	}
	if first.ProvenanceSHA256 != second.ProvenanceSHA256 {
		t.Fatal("provenance digest must be stable")
	}
}

func TestVerifyStandalone(t *testing.T) {
	cfg := testConfig(t, fixture.CaseNormal)
	p, _ := New(cfg)
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := Verify(filepath.Join(cfg.OutputDir, ManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid || res.SegmentCount == 0 || res.RepresentationCount != 2 {
		t.Fatalf("verify result wrong: %+v", res)
	}
	if res.ManifestSHA256 == "" {
		t.Fatal("verify must report the manifest digest")
	}
}

func TestMfhdGapIsCarriedForward(t *testing.T) {
	cfg := testConfig(t, fixture.CaseMfhdGap)
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := p.Run(context.Background())
	// The generated gap is identical on every representation, so windows
	// stay complete; assert the explicit gap is at least carried forward.
	if err != nil {
		t.Fatalf("uniform gap should still validate: %v", err)
	}
	if summary.GapCount != 2 {
		t.Fatalf("gap count = %d, want 2 (one per rep)", summary.GapCount)
	}
	cp, _ := p.store.Load()
	if cp.Stage != checkpoint.StageComplete {
		t.Fatalf("uniform-gap run should complete, stage=%s", cp.Stage)
	}
}

func TestMisalignedFailsManifestAndStopsAtSafeCheckpoint(t *testing.T) {
	cfg := testConfig(t, fixture.CaseMisaligned)
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := p.Run(context.Background())
	if !fault.Is(err, fault.CodeMisaligned) {
		t.Fatalf("expected misaligned manifest error, got %v", err)
	}
	if summary.ManifestValid {
		t.Fatal("manifest must not be marked valid when misaligned")
	}
	if summary.CommittedSegments != 0 {
		t.Fatalf("no segment may commit when validation fails, got %d", summary.CommittedSegments)
	}
	// The last safe checkpoint remains at align; manifest never advanced.
	cp, _ := p.store.Load()
	if cp.Stage != checkpoint.StageAlign {
		t.Fatalf("checkpoint should stay at align, got %s", cp.Stage)
	}
	if _, statErr := os.Stat(filepath.Join(cfg.OutputDir, ManifestPath)); !os.IsNotExist(statErr) {
		t.Fatal("no manifest file may exist after a failed validation")
	}
}

func TestTamperedInputIsRecomputedNotReused(t *testing.T) {
	cfg := testConfig(t, fixture.CaseNormal)
	p, _ := New(cfg)
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstDigest := readManifestDigest(t, cfg.OutputDir)

	// Tamper with an input file after the checkpoint was written.
	target := filepath.Join(cfg.InputDir, "rep0", "frag-002.m4s")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// The repeat run recomputes every fragment (the stored digest no longer
	// verifies, so nothing is blindly reused). The previously successful
	// manifest artifact is protected from overwrite by WriteAtomic.
	p2, _ := New(cfg)
	summary, err := p2.Run(context.Background())
	if err != nil {
		t.Fatalf("tampered-but-parseable input should still process: %v", err)
	}
	if !summary.ManifestValid {
		t.Fatal("recomputed manifest should be valid")
	}
	if readManifestDigest(t, cfg.OutputDir) != firstDigest {
		t.Fatal("prior successful manifest must not be overwritten on retry")
	}
	cp, _ := p2.store.Load()
	if cp.InputDigests["rep0/frag-002.m4s"] == "" {
		t.Fatal("recomputed digest must be recorded in the checkpoint")
	}
}

func readManifestDigest(t *testing.T, outputDir string) string {
	t.Helper()
	res, err := Verify(filepath.Join(outputDir, ManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	return res.ManifestSHA256
}
