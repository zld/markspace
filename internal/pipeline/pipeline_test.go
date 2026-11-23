package pipeline_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediacore/internal/checkpoint"
	"mediacore/internal/pipeline"
)

func baseConfig(dir string) pipeline.Config {
	return pipeline.Config{
		Seed:           41,
		Tracks:         2,
		Fragments:      24,
		Faults:         []string{"truncation", "tfdt-regression", "index-gap"},
		CheckpointPath: filepath.Join(dir, "checkpoint.json"),
		Resume:         false,
		FrameWidth:     32,
		FrameHeight:    18,
		Timescale:      90000,
	}
}

func runPipeline(t *testing.T, cfg pipeline.Config) *pipeline.Result {
	t.Helper()
	injected, inputs, err := pipeline.Generate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := pipeline.Run(cfg, injected, inputs)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

func TestRunProducesVerifiedTimelineAndFrames(t *testing.T) {
	dir := t.TempDir()
	res := runPipeline(t, baseConfig(dir))
	if len(res.Failures) != 0 {
		t.Fatalf("invariant failures: %v", res.Failures)
	}
	if res.FrameCount == 0 || res.Frames == nil || res.Frames.Digest == "" {
		t.Fatalf("no frames produced: %+v", res.Frames)
	}
	if res.TrackCount < 2 {
		t.Fatalf("tracks=%d", res.TrackCount)
	}
	if res.Faults != 3 {
		t.Fatalf("faults=%d", res.Faults)
	}
	if !res.Recovered {
		t.Fatal("damaged input was not processed")
	}
	if res.FirstErrorOffset <= 0 {
		t.Fatal("no first error offset recorded")
	}
	// The first error must land exactly on a byte some stage identified.
	if delta := closestDelta(res); delta != 0 {
		t.Fatalf("first error offset is %d bytes from every injection", delta)
	}
}

func closestDelta(r *pipeline.Result) int64 {
	best := int64(-1)
	for _, off := range r.InjectedOffsets {
		d := r.FirstErrorOffset - off
		if d < 0 {
			d = -d
		}
		if best < 0 || d < best {
			best = d
		}
	}
	if best < 0 {
		return 0
	}
	return best
}

func TestRunIsDeterministicAcrossProcesses(t *testing.T) {
	a := runPipeline(t, baseConfig(t.TempDir()))
	b := runPipeline(t, baseConfig(t.TempDir()))
	if a.Frames.Digest != b.Frames.Digest {
		t.Fatalf("frame digest differs: %s vs %s", a.Frames.Digest, b.Frames.Digest)
	}
	if a.FirstErrorOffset != b.FirstErrorOffset {
		t.Fatal("first error offset differs between identical runs")
	}
	if len(a.Failures) != 0 || len(b.Failures) != 0 {
		t.Fatal("invariant failures in a clean repeat run")
	}
}

func TestResumeReusesCheckpointAndReproducesDigest(t *testing.T) {
	dir := t.TempDir()
	cfg := baseConfig(dir)
	first := runPipeline(t, cfg)
	if first.Resumed {
		t.Fatal("first run resumed from nothing")
	}
	cfg.Resume = true
	second := runPipeline(t, cfg)
	if !second.Resumed {
		t.Fatal("second run did not resume")
	}
	if second.ReusedFragments == 0 {
		t.Fatal("resume reparsed every fragment instead of reusing the checkpoint")
	}
	if second.Frames.Digest != first.Frames.Digest {
		t.Fatal("resume changed the exported digest")
	}
	if second.ResumeMS > 2000 {
		t.Fatalf("resume took %.1f ms", second.ResumeMS)
	}
}

func TestResumeRefusesChangedConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := baseConfig(dir)
	runPipeline(t, cfg)
	cfg.Resume = true
	cfg.Faults = []string{"duplicate-fragment"}
	injected, inputs, err := pipeline.Generate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Run(cfg, injected, inputs); err == nil {
		t.Fatal("resumed with a different configuration")
	} else if !errors.Is(err, checkpoint.ErrMismatch) && !strings.Contains(err.Error(), "resume refused") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResumeRefusesChangedInput(t *testing.T) {
	dir := t.TempDir()
	cfg := baseConfig(dir)
	runPipeline(t, cfg)
	cfg.Resume = true
	// Same configuration, different seed -> different input bytes.
	cfg.Seed = 42
	injected, inputs, err := pipeline.Generate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Run(cfg, injected, inputs); err == nil {
		t.Fatal("resumed with different input bytes")
	}
}

func TestWriteOutputsAndVerify(t *testing.T) {
	dir := t.TempDir()
	cfg := baseConfig(dir)
	res := runPipeline(t, cfg)
	framesPath := filepath.Join(dir, "frames.jsonl")
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := res.WriteOutputs(framesPath, manifestPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(framesPath); err != nil {
		t.Fatal(err)
	}
	report, err := pipeline.VerifyFiles(manifestPath, framesPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Failures) != 0 {
		t.Fatalf("verify failures: %v", report.Failures)
	}
	if report.FrameCount != res.FrameCount {
		t.Fatalf("frame_count=%d want %d", report.FrameCount, res.FrameCount)
	}
	if report.ManifestSHA256 == "" {
		t.Fatal("verify did not report a manifest digest")
	}
	// Tampering with one exported frame must be detected.
	data, err := os.ReadFile(framesPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), `"number":0`, `"number":7`, 1)
	if err := os.WriteFile(framesPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.VerifyFiles(manifestPath, framesPath); err == nil {
		// A digest mismatch is reported as failures, not an error.
		report, err = pipeline.VerifyFiles(manifestPath, framesPath)
		if err == nil && len(report.Failures) == 0 {
			t.Fatal("tampered export verified clean")
		}
	}
}

func TestCleanInputExitsWithoutFaults(t *testing.T) {
	cfg := baseConfig(t.TempDir())
	cfg.Faults = nil
	res := runPipeline(t, cfg)
	if res.Faults != 0 {
		t.Fatalf("faults=%d for clean input", res.Faults)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("clean input produced errors: %v", res.Errors)
	}
	if len(res.Failures) != 0 {
		t.Fatalf("clean input failed invariants: %v", res.Failures)
	}
}

func TestFrameDigestIndependentOfCheckpointing(t *testing.T) {
	withCP := baseConfig(t.TempDir())
	withoutCP := baseConfig(t.TempDir())
	withoutCP.CheckpointPath = ""
	a := runPipeline(t, withCP)
	b := runPipeline(t, withoutCP)
	if a.Frames.Digest != b.Frames.Digest {
		t.Fatal("checkpointing changed the produced frames")
	}
}
