package orchestrator

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"transcode/internal/fixture"
	"transcode/internal/model"
	"transcode/internal/state"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	// <root>/internal/orchestrator/engine_test.go
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func buildEncoder(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "stubencoder")
	runGo(t, moduleRoot(t), "build", "-o", bin, "./cmd/stubencoder")
	return bin
}

func setupJob(t *testing.T) (Config, string) {
	t.Helper()
	root := t.TempDir()
	in := filepath.Join(root, "sample.vid")
	samples := filepath.Join(root, "samples.txt")
	if _, _, err := fixture.Generate(fixture.Spec{
		Path: in, SidecarPath: samples,
		DurationSec: 12, FPS: 10, Width: 8, Height: 6,
	}); err != nil {
		t.Fatal(err)
	}
	sampleVals, err := fixture.LoadSamples(samples)
	if err != nil {
		t.Fatal(err)
	}
	bin := buildEncoder(t, root)
	cfg := Config{
		JobID: "job-test", WorkspaceRoot: root, InputPath: in,
		OutputDir:      filepath.Join(root, "out"),
		WorkDir:        filepath.Join(root, "work"),
		CheckpointPath: filepath.Join(root, "cp", "job.json"),
		DurationSec:    12, SegmentSec: 3, TargetKbps: 800, MinKbps: 50,
		Workers: 2, MaxOutputBytes: 1 << 20,
		EncoderTemplate: bin + " --in {in} --out {out} --target-kbps {kbps} --duration-sec {duration}",
		Samples:         sampleVals,
		Params: state.Params{
			InputPath: in, DurationSec: 12, SegmentSec: 3,
			TargetKbps: 800, MinKbps: 50, Workers: 2,
			EncoderCommand: bin, MaxOutputBytes: 1 << 20,
		},
	}
	return cfg, root
}

func TestEngineFullRunSucceeds(t *testing.T) {
	cfg, _ := setupJob(t)
	eng, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Task.Segments) != 4 {
		t.Fatalf("want 4 segments, got %d", len(res.Task.Segments))
	}
	if res.Failed != 0 || res.Succeeded != 4 || res.Interrupted != 0 {
		t.Fatalf("counts wrong: ok=%d failed=%d interrupted=%d", res.Succeeded, res.Failed, res.Interrupted)
	}
	if res.Quality.MeanPSNRDB < 30 || res.Quality.MeanPSNRDB > 45 {
		t.Fatalf("PSNR mean %.2f outside 30-45 dB", res.Quality.MeanPSNRDB)
	}
	if len(res.Quality.SegmentPSNRDB) != 4 || len(res.Quality.SegmentDeltaDB) != 3 {
		t.Fatalf("report arrays wrong: %+v", res.Quality)
	}
	for _, seg := range res.Task.Segments {
		if seg.Encode == nil || seg.Quality == nil {
			t.Fatalf("segment %d missing results", seg.Index)
		}
		if absF(seg.Encode.Deviation) > 5.0 {
			t.Fatalf("segment %d deviation %.2f%%", seg.Index, seg.Encode.Deviation)
		}
	}
}

func TestEngineResumeSkipsCompletedSegments(t *testing.T) {
	cfg, _ := setupJob(t)
	eng, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first, err := eng.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Second engine, same checkpoint: every segment must be resumed, none run.
	eng2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := eng2.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !second.Recovery.Resumed {
		t.Fatal("second run should resume from checkpoint")
	}
	for i, seg := range second.Task.Segments {
		if seg.Status != model.StatusSucceeded {
			t.Fatalf("segment %d not resumed as succeeded", i)
		}
		if seg.Attempts != first.Task.Segments[i].Attempts {
			t.Fatalf("segment %d was re-encoded on resume", i)
		}
	}
}

func TestEngineRejectsEncoderOutsideWorkspace(t *testing.T) {
	cfg, _ := setupJob(t)
	cfg.EncoderTemplate = ""
	if _, err := New(cfg); err == nil {
		t.Fatal("missing encoder command must be rejected")
	}
}

func TestEngineRejectsPathEscape(t *testing.T) {
	cfg, _ := setupJob(t)
	cfg.CheckpointPath = filepath.Join("..", "escape.json")
	if _, err := New(cfg); err == nil {
		t.Fatal("checkpoint escaping workspace must be rejected")
	}
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
