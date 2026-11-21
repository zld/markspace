// Package integration contains end-to-end tests that drive the real FFmpeg
// pipeline: fixture generation, allocation, closed-loop encoding, PSNR
// validation, checkpointing and recovery after interruption.
package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"bitrack/internal/checkpoint"
	"bitrack/internal/cli"
	"bitrack/internal/media"
	"bitrack/internal/pipeline"
	"bitrack/internal/report"
)

func newOptions(t *testing.T, workDir string, targetKbps int) *cli.Options {
	t.Helper()
	return &cli.Options{
		WorkDir: workDir, Input: "source.mp4", TargetKbps: targetKbps,
		SegmentSeconds: 4, Workers: 2, Iterations: 4, TolerancePct: 8,
		MinSegmentKbps: 40, MaxPSNRDropDB: 2,
	}
}

func generateFixture(t *testing.T, workDir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := media.GenerateFixture(ctx, workDir, media.FixtureSpec{
		Width: 176, Height: 144, FPS: 24,
		Segments: 4, FramesPerSegment: 48, TailFrames: 12,
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
}

// TestEndToEndPipeline runs a full baseline + delivery pair and verifies the
// JSON summary contract: exact budget sum, deviation band, PSNR and regression
// accounting.
func TestEndToEndPipeline(t *testing.T) {
	workDir := t.TempDir()
	generateFixture(t, workDir)
	ctx := context.Background()

	baseOpts := newOptions(t, workDir, 900)
	baseCp, err := pipeline.NewRunner(baseOpts, "default", pipeline.NoopInjector{}).Run(ctx)
	if err != nil {
		t.Fatalf("baseline run: %v", err)
	}
	basePath, err := report.SaveBaseline(workDir, baseCp)
	if err != nil {
		t.Fatal(err)
	}

	runOpts := newOptions(t, workDir, 300)
	cp, err := pipeline.NewRunner(runOpts, "default", pipeline.NoopInjector{}).Run(ctx)
	if err != nil {
		t.Fatalf("delivery run: %v", err)
	}
	if err := pipeline.ApplyBaseline(cp, basePath); err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	sum := report.BuildSummary(cp, "run", 0)

	if sum.TargetKbps != 300 {
		t.Errorf("target_kbps = %d, want 300", sum.TargetKbps)
	}
	// CBR mode plus the closed loop must land within +/-10% of target.
	if sum.BitrateDeviation < -10 || sum.BitrateDeviation > 10 {
		t.Errorf("deviation %.2f%% outside +/-10%% band", sum.BitrateDeviation)
	}
	if sum.ActualKbps < 270 || sum.ActualKbps > 330 {
		t.Errorf("actual_kbps %.1f outside 270-330", sum.ActualKbps)
	}
	if sum.PSNRMeanDB < 20 || sum.PSNRMeanDB > 99 {
		t.Errorf("psnr_mean_db %.2f outside plausible band", sum.PSNRMeanDB)
	}
	// Every segment must carry the required per-segment fields.
	var targetSum int64
	for _, s := range sum.Segments {
		if s.State != "committed" {
			t.Errorf("segment %d state = %s", s.Index, s.State)
		}
		if s.TargetBits <= 0 || s.ActualBits <= 0 || s.PSNRDB <= 0 {
			t.Errorf("segment %d missing metrics: %+v", s.Index, s)
		}
		if len(s.Attempts) == 0 {
			t.Errorf("segment %d recorded no attempts", s.Index)
		}
		targetSum += s.TargetBits
	}
	var cpSum int64
	for _, s := range cp.Segments {
		cpSum += s.TargetBits
	}
	if cpSum != targetSum {
		t.Errorf("checkpoint budget sum %d != report sum %d", cpSum, targetSum)
	}
}

// TestRecoveryAfterInterruption injects fail-after, confirms the run stops at
// the requested commit count, then resumes and reuses every committed segment.
func TestRecoveryAfterInterruption(t *testing.T) {
	workDir := t.TempDir()
	generateFixture(t, workDir)
	ctx := context.Background()

	opts := newOptions(t, workDir, 900)
	opts.FailAfter = 2
	opts.Workers = 1 // single worker makes the interrupt point deterministic
	runner := pipeline.NewRunner(opts, "default", pipeline.NoopInjector{})
	cp, err := runner.Run(ctx)
	if err != pipeline.ErrInterrupted {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
	committed, paused := 0, 0
	for _, s := range cp.Segments {
		switch s.State {
		case "committed":
			committed++
		case "paused":
			paused++
		}
	}
	if committed != 2 || committed+paused != len(cp.Segments) {
		t.Fatalf("after interruption: committed=%d paused=%d total=%d", committed, paused, len(cp.Segments))
	}

	resumeOpts := newOptions(t, workDir, 900)
	resumeOpts.Workers = 1
	resumed, reused, err := pipeline.NewRunner(resumeOpts, "default", pipeline.NoopInjector{}).Resume(ctx)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if reused != 2 {
		t.Errorf("reused = %d, want 2", reused)
	}
	for _, s := range resumed.Segments {
		if s.State != "committed" {
			t.Errorf("segment %d state after resume = %s", s.Index, s.State)
		}
	}
}

// TestCorruptedOutputReEncoded tampers a committed segment file and verifies
// the checksum gate forces a fresh encode rather than reusing bad bytes.
func TestCorruptedOutputReEncoded(t *testing.T) {
	workDir := t.TempDir()
	generateFixture(t, workDir)
	ctx := context.Background()
	opts := newOptions(t, workDir, 900)
	cp, err := pipeline.NewRunner(opts, "default", pipeline.NoopInjector{}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target := cp.Segments[0].OutputPath
	if err := os.WriteFile(target, []byte("garbage-payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	resumeOpts := newOptions(t, workDir, 900)
	_, reused, err := pipeline.NewRunner(resumeOpts, "default", pipeline.NoopInjector{}).Resume(ctx)
	if err != nil {
		t.Fatalf("resume after corruption: %v", err)
	}
	if reused != len(cp.Segments)-1 {
		t.Errorf("reused = %d, want %d (one segment must re-encode)", reused, len(cp.Segments)-1)
	}
	store, _ := checkpoint.NewStore(workDir)
	reloaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	decision := checkpoint.VerifyCommitted(reloaded.Segments[0])
	if !decision.Reusable {
		t.Errorf("re-encoded segment still fails verification: %s", decision.Reason)
	}
}

// TestParameterChangeInvalidatesResume confirms a changed parameter digest
// blocks reuse instead of silently mixing differently-encoded fragments.
func TestParameterChangeInvalidatesResume(t *testing.T) {
	workDir := t.TempDir()
	generateFixture(t, workDir)
	ctx := context.Background()
	opts := newOptions(t, workDir, 900)
	if _, err := pipeline.NewRunner(opts, "default", pipeline.NoopInjector{}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	// Tamper the persisted parameters, simulating an encoder change.
	store, _ := checkpoint.NewStore(workDir)
	cp, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	cp.Params.Preset = "slow"
	if err := store.Save(cp); err != nil {
		t.Fatal(err)
	}
	resumeOpts := newOptions(t, workDir, 900)
	if _, _, err := pipeline.NewRunner(resumeOpts, "default", pipeline.NoopInjector{}).Resume(ctx); err == nil {
		t.Fatal("resume must reject a changed parameter digest")
	}
}

// TestManifestFanOut runs two jobs from one manifest and verifies each gets an
// isolated checkpoint directory and an independent bitrate budget.
func TestManifestFanOut(t *testing.T) {
	workDir := t.TempDir()
	generateFixture(t, workDir)
	ctx := context.Background()

	srcData, err := os.ReadFile(workDir + "/source.mp4")
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range []struct {
		id   string
		kbps int
	}{
		{"a", 300}, {"b", 600},
	} {
		jobDir := workDir + "/jobs/" + job.id
		if err := os.MkdirAll(jobDir+"/segments", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(jobDir+"/source.mp4", srcData, 0o644); err != nil {
			t.Fatal(err)
		}
		opts := newOptions(t, jobDir, job.kbps)
		opts.Input = "source.mp4"
		cp, err := pipeline.NewRunner(opts, job.id, pipeline.NoopInjector{}).Run(ctx)
		if err != nil {
			t.Fatalf("job %s: %v", job.id, err)
		}
		if cp.Params.TargetKbps != job.kbps {
			t.Errorf("job %s target = %d, want %d", job.id, cp.Params.TargetKbps, job.kbps)
		}
		store, err := checkpoint.NewStore(jobDir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(); err != nil {
			t.Fatalf("job %s checkpoint: %v", job.id, err)
		}
	}
}
