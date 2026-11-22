package pipeline_test

import (
	"path/filepath"
	"testing"

	"mediacheck/internal/ebml"
	"mediacheck/internal/fixture"
	"mediacheck/internal/pipeline"
)

func baseCfg(t *testing.T) pipeline.Config {
	t.Helper()
	return pipeline.Config{
		Seed: 7, Rates: 3, Clusters: 12, CheckpointEvery: 4,
		WorkDir:         t.TempDir(),
		TargetSegmentNS: 1_000_000_000,
		MaxSkewNS:       20_000_000,
		DriftLimitPPM:   500,
	}
}

func TestCleanRunProducesDeterministicManifest(t *testing.T) {
	cfg := baseCfg(t)
	res, err := pipeline.Run(cfg)
	if err != nil {
		t.Fatalf("clean run: %v", err)
	}
	if res.Representations != 3 || res.SegmentsEach != 12 {
		t.Fatalf("reps=%d segments=%d want 3/12", res.Representations, res.SegmentsEach)
	}
	if len(res.ManifestSHA) != 64 {
		t.Fatalf("manifest sha len=%d", len(res.ManifestSHA))
	}
	// Repeated identical run yields the same checksum.
	cfg2 := cfg
	cfg2.WorkDir = t.TempDir()
	res2, err := pipeline.Run(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if res2.ManifestSHA != res.ManifestSHA {
		t.Fatalf("non-deterministic manifest: %s != %s", res2.ManifestSHA, res.ManifestSHA)
	}
}

func TestClusterTruncationPropagatesAsMaterialError(t *testing.T) {
	cfg := baseCfg(t)
	cfg.Corrupt = fixture.CorruptClusterTruncate
	res, err := pipeline.Run(cfg)
	if err == nil {
		t.Fatal("truncated material must fail")
	}
	pe, ok := err.(*pipeline.Error)
	if !ok || pe.Exit != pipeline.ExitMaterial {
		t.Fatalf("want material exit 2, got %v", err)
	}
	if res.FirstErrorClass != ebml.ErrTruncated || res.FirstErrorOffset < 0 {
		t.Fatalf("class=%s offset=%d want TRUNCATED with offset", res.FirstErrorClass, res.FirstErrorOffset)
	}
	// No manifest may be committed for corrupt material.
	if res.Manifest != nil || res.ManifestSHA != "" {
		t.Fatal("corrupt material must not produce a manifest")
	}
}

func TestMissingTrackPropagatesAndBlocksCommit(t *testing.T) {
	cfg := baseCfg(t)
	cfg.Corrupt = fixture.CorruptMissingTrack
	_, err := pipeline.Run(cfg)
	if err == nil {
		t.Fatal("unknown track reference must fail closed")
	}
	pe := err.(*pipeline.Error)
	if pe.Exit != pipeline.ExitMaterial || pe.Class != ebml.ErrBadElement {
		t.Fatalf("want BAD_ELEMENT material error, got %v", err)
	}
}

func TestWrapDriftAmbiguityRejected(t *testing.T) {
	cfg := baseCfg(t)
	cfg.Corrupt = fixture.CorruptWrapDrift
	_, err := pipeline.Run(cfg)
	if err == nil {
		t.Fatal("half-period timestamp jump must be rejected")
	}
	pe := err.(*pipeline.Error)
	if pe.Exit != pipeline.ExitMaterial {
		t.Fatalf("want material exit, got %v", err)
	}
}

func TestDryRunWritesNoCheckpoints(t *testing.T) {
	cfg := baseCfg(t)
	cfg.DryRun = true
	res, err := pipeline.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(cfg.WorkDir, "*.chk"))
	if len(matches) != 0 {
		t.Fatalf("dry run wrote %d checkpoint files", len(matches))
	}
	if res.ManifestSHA == "" {
		t.Fatal("dry run still produces a manifest")
	}
}

func TestInterruptAndResumeYieldsSameManifest(t *testing.T) {
	work := t.TempDir()
	cfg := baseCfg(t)
	cfg.WorkDir = work
	cfg.InterruptAfter = 8

	// Interrupted run fails at the simulated crash.
	_, err := pipeline.Run(cfg)
	if err == nil {
		t.Fatal("interrupted run must return an error")
	}
	pe := err.(*pipeline.Error)
	if pe.Exit != pipeline.ExitIndex {
		t.Fatalf("interrupt should surface as index/exit 3 in this kernel, got %d", pe.Exit)
	}

	// Resume from the same working directory.
	resume := cfg
	resume.Resume = true
	resume.InterruptAfter = 0
	res, err := pipeline.Run(resume)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if res.ReplayedChunks > 4 {
		t.Fatalf("replayed_chunks=%d must be <= checkpoint-every (4)", res.ReplayedChunks)
	}

	// Uninterrupted baseline must match the resumed output exactly.
	baseline, err := pipeline.Run(baseCfg(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.ManifestSHA != baseline.ManifestSHA {
		t.Fatalf("resumed manifest %s != baseline %s", res.ManifestSHA, baseline.ManifestSHA)
	}
}

func TestUnknownNestingFixtureStillParses(t *testing.T) {
	cfg := baseCfg(t)
	cfg.Corrupt = fixture.CorruptUnknownNesting
	res, err := pipeline.Run(cfg)
	if err != nil {
		t.Fatalf("unknown-length nesting is a valid (not corrupt) fixture: %v", err)
	}
	if res.SegmentsEach != 12 {
		t.Fatalf("segments=%d want 12", res.SegmentsEach)
	}
}
