package pipeline

import (
	"path/filepath"
	"testing"

	"avbatch/internal/index"
	"avbatch/internal/mediaerr"
	"avbatch/internal/synth"
)

// buildGroup makes the demo group without touching the index.
func buildGroup(t *testing.T, opt synth.Options) Input {
	t.Helper()
	g, err := synth.BuildDemoGroup(opt)
	if err != nil {
		t.Fatalf("build group: %v", err)
	}
	if g.TargetKbps == 0 {
		g.TargetKbps = 1000
	}
	return FromGroup(g)
}

func cleanCfg(dir string) Config {
	cfg := DefaultConfig()
	cfg.IndexPath = filepath.Join(dir, "manifest.idx")
	return cfg
}

func TestRunCleanInput(t *testing.T) {
	in := buildGroup(t, synth.Options{Seed: 41})
	res, err := Run(in, cleanCfg(t.TempDir()))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Segments < 5 {
		t.Fatalf("segments = %d", res.Segments)
	}
	if res.QualitySamples == 0 {
		t.Fatal("no samples processed")
	}
	if res.DeviationPct < 0 || res.DeviationPct > 15 {
		t.Fatalf("deviation = %.2f%%, want 0..15", res.DeviationPct)
	}
	if res.AlignmentErrorMS > 40 {
		t.Fatalf("alignment error = %.2f ms, want <= 40", res.AlignmentErrorMS)
	}
	if res.ManifestSHA256 == "" {
		t.Fatal("manifest digest missing")
	}
	// Every generated group re-delivers one fragment, so the dedup path is
	// always exercised; collisions must not appear on identical content.
	if res.Duplicates != 1 || res.Collisions != 0 {
		t.Fatalf("duplicates=%d collisions=%d, want 1 and 0", res.Duplicates, res.Collisions)
	}
	if res.RTPPackets == 0 {
		t.Fatal("no RTP packets were delivered")
	}
	// The generator injects 40..160 ppm of sender clock error plus bounded
	// arrival jitter; the estimate must find that drift, not just report zero.
	if res.DriftPPM < 20 || res.DriftPPM > 1000 {
		t.Fatalf("drift = %.1f ppm, want a positive estimate near the injected 40..160 ppm", res.DriftPPM)
	}
}

func TestRunIsDeterministic(t *testing.T) {
	in := buildGroup(t, synth.Options{Seed: 41, InjectVFR: true})
	dir := t.TempDir()
	cfg := cleanCfg(dir)
	first, err := Run(in, cfg)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	cfg.IndexPath = filepath.Join(dir, "second.idx")
	second, err := Run(in, cfg)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if first.ManifestSHA256 != second.ManifestSHA256 {
		t.Fatalf("manifest digests differ: %s vs %s", first.ManifestSHA256, second.ManifestSHA256)
	}
	if len(first.Entries) != len(second.Entries) {
		t.Fatalf("entry counts differ: %d vs %d", len(first.Entries), len(second.Entries))
	}
}

func TestRunSequenceGapPropagatesToManifest(t *testing.T) {
	in := buildGroup(t, synth.Options{Seed: 11, InjectSeqGap: true})
	res, err := Run(in, cleanCfg(t.TempDir()))
	if err == nil {
		t.Fatal("sequence gap was not reported")
	}
	if !mediaerr.Is(err, mediaerr.KindSequenceGap) {
		t.Fatalf("err = %v, want sequence_gap", err)
	}
	if !res.Recovered {
		t.Fatal("output should still be usable")
	}
	if res.Segments == 0 || len(res.Entries) == 0 {
		t.Fatal("no manifest produced after a sequence gap")
	}
}

func TestRunTruncatedBoxYieldsRecoverableOutput(t *testing.T) {
	in := buildGroup(t, synth.Options{Seed: 41, InjectTruncatedBox: true})
	res, err := Run(in, cleanCfg(t.TempDir()))
	if !mediaerr.Is(err, mediaerr.KindTruncatedBox) {
		t.Fatalf("err = %v, want truncated_box", err)
	}
	if !res.TruncatedInput {
		t.Fatal("truncation flag not set")
	}
	if res.Entries == nil {
		t.Fatal("truncated input produced no recoverable manifest")
	}
}

func TestRunDuplicateFragmentIsDetected(t *testing.T) {
	// The generator re-delivers one fragment byte-identically; the pipeline
	// must classify it as a duplicate, drop it before cutting, and still
	// produce a complete manifest.
	in := buildGroup(t, synth.Options{Seed: 41})
	dir := t.TempDir()
	res, err := Run(in, cleanCfg(dir))
	if err != nil {
		t.Fatalf("a re-delivered fragment is not damage: %v", err)
	}
	if len(res.Notes) == 0 {
		t.Fatal("duplicate was not reported")
	}
	if res.Duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", res.Duplicates)
	}
	if res.Segments == 0 {
		t.Fatal("duplicate handling lost the manifest")
	}
}

func TestRunTfdtRegressionRepairedWithOneError(t *testing.T) {
	in := buildGroup(t, synth.Options{Seed: 41, InjectTfdtRegression: true})
	res, err := Run(in, cleanCfg(t.TempDir()))
	if !mediaerr.Is(err, mediaerr.KindTfdtRegression) {
		t.Fatalf("err = %v, want tfdt_regression", err)
	}
	// The whole shifted tail must be repaired from a single detection, and the
	// repaired timeline must not contain overlaps.
	count := 0
	for _, e := range mediaerr.Collect(err) {
		if e.Kind == mediaerr.KindTfdtRegression {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("regression reported %d times, want 1", count)
	}
	if res.Segments == 0 {
		t.Fatal("no manifest after repair")
	}
}

func TestRunRTPWrapUnwrapped(t *testing.T) {
	in := buildGroup(t, synth.Options{Seed: 41, InjectWrap: true})
	res, err := Run(in, cleanCfg(t.TempDir()))
	if err != nil {
		t.Fatalf("wrap caused a failure: %v", err)
	}
	if res.WrapCount < 1 {
		t.Fatalf("wrap count = %d", res.WrapCount)
	}
	if res.RTPAnomalies != 0 {
		t.Fatalf("wrap produced %d anomalies", res.RTPAnomalies)
	}
}

func TestRunCheckpointCrashRecovers(t *testing.T) {
	dir := t.TempDir()
	in := buildGroup(t, synth.Options{Seed: 41})
	cfg := cleanCfg(dir)
	cfg.CrashAtBlocks = 2
	res, err := Run(in, cfg)
	if err != nil {
		t.Fatalf("recovered run returned an error: %v", err)
	}
	if res.RecoveredBlocks < 2 {
		t.Fatalf("recovered blocks = %d, want at least 2", res.RecoveredBlocks)
	}
	if res.ResumeOffset <= 0 {
		t.Fatalf("resume offset = %d", res.ResumeOffset)
	}
	// After recovery the published index must be complete and clean.
	r, err := index.Open(cfg.IndexPath, Digest(in, cfg))
	if err != nil {
		t.Fatalf("reopen recovered index: %v", err)
	}
	if len(r.Entries) != len(res.Entries) {
		t.Fatalf("recovered index holds %d entries, want %d", len(r.Entries), len(res.Entries))
	}
}

func TestRunResumeRefusesChangedInput(t *testing.T) {
	dir := t.TempDir()
	in := buildGroup(t, synth.Options{Seed: 41})
	cfg := cleanCfg(dir)
	if _, err := Run(in, cfg); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	// Same path, different media, and the run starts from an existing
	// checkpoint: the stored digest must stop the resume rather than appending
	// entries from another capture.
	changed := buildGroup(t, synth.Options{Seed: 42})
	res, err := Run(changed, cfg)
	if !mediaerr.Is(err, mediaerr.KindResumeMismatch) {
		t.Fatalf("err = %v, want resume_mismatch", err)
	}
	if !res.ResumeRefused {
		t.Fatal("resume refusal not reported")
	}
}

func TestDigestChangesWithMediaAndConfig(t *testing.T) {
	a := buildGroup(t, synth.Options{Seed: 41})
	b := buildGroup(t, synth.Options{Seed: 42})
	cfg := DefaultConfig()
	if Digest(a, cfg) == Digest(b, cfg) {
		t.Fatal("different media produced the same digest")
	}
	c := cfg
	c.SegmentMillis = 500
	if Digest(a, cfg) == Digest(a, c) {
		t.Fatal("config change did not affect the digest")
	}
}

func TestRunEmptyInputFailsCleanly(t *testing.T) {
	_, err := Run(Input{}, cleanCfg(t.TempDir()))
	if err == nil {
		t.Fatal("empty input accepted")
	}
}
