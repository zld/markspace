package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func baseCfg(t *testing.T, dir string) Config {
	t.Helper()
	return Config{
		Duration: 2 * time.Second, FPS: 25, TargetBitrate: 2_400_000,
		QueueCapacity: 4, OutputDir: dir, BitrateTolPct: 5,
	}
}

func TestRunSuccessPath(t *testing.T) {
	cfg := baseCfg(t, filepath.Join(t.TempDir(), "ok"))
	rep, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Frames != 50 {
		t.Fatalf("frames=%d want 50", rep.Frames)
	}
	if rep.ManifestMaxErr != 0 || rep.ManifestSHA == "" {
		t.Fatalf("manifest not aligned/sealed: %+v", rep)
	}
	if rep.State != "complete" || len(rep.Errors) != 0 {
		t.Fatalf("healthy run must be complete without errors: state=%s errors=%d", rep.State, len(rep.Errors))
	}
	if rep.BitrateDeltaPct != 0 {
		t.Fatalf("planned bitrate must hit target exactly, delta=%f", rep.BitrateDeltaPct)
	}
}

func TestRunDeterministicManifestHash(t *testing.T) {
	r1, err := Run(context.Background(), baseCfg(t, filepath.Join(t.TempDir(), "a")))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Run(context.Background(), baseCfg(t, filepath.Join(t.TempDir(), "b")))
	if err != nil {
		t.Fatal(err)
	}
	if r1.ManifestSHA != r2.ManifestSHA {
		t.Fatalf("full reruns must hash identically: %s vs %s", r1.ManifestSHA, r2.ManifestSHA)
	}
}

func TestRunCorruptPathIsolatesBadFrame(t *testing.T) {
	cfg := baseCfg(t, filepath.Join(t.TempDir(), "corrupt"))
	cfg.Fixture = "corrupt"
	rep, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.State != "corrupt" || len(rep.Errors) != 1 {
		t.Fatalf("corrupt run must record one isolated error, state=%s errors=%d", rep.State, len(rep.Errors))
	}
	if rep.FirstBadTimeNS <= 0 {
		t.Fatal("first bad time must be reported")
	}
	if rep.Frames != 50 {
		t.Fatalf("all frames still rendered, got %d", rep.Frames)
	}
}

func TestRunTruncatedFailsWithoutRecover(t *testing.T) {
	cfg := baseCfg(t, filepath.Join(t.TempDir(), "trunc"))
	cfg.Fixture = "truncated"
	if _, err := Run(context.Background(), cfg); err == nil {
		t.Fatal("truncated input without recovery must propagate parse failure")
	}
}

func TestRunRecoveryCompletesWithoutDuplicates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recover")
	cfg := baseCfg(t, dir)
	cfg.Fixture = "truncated"
	cfg.Recover = true
	rep, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Frames != 50 {
		t.Fatalf("recovery must conserve all frames, got %d", rep.Frames)
	}
	// Interruption was at framesN/3; only the remainder counts as recovered work.
	if rep.RecoveredFrames != 50-50/3 {
		t.Fatalf("recovered_frames=%d want %d", rep.RecoveredFrames, 50-50/3)
	}
	if rep.State != "corrupt" || rep.FirstBadTimeNS <= 0 {
		t.Fatalf("recovery must retain corrupt report: state=%s firstBad=%d", rep.State, rep.FirstBadTimeNS)
	}
	// A second recovery invocation from the persisted checkpoint reuses it and
	// never re-submits completed frames.
	rep2, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Frames != 50 || rep2.RecoveredFrames != 0 {
		t.Fatalf("resume from existing checkpoint must not reprocess: frames=%d recovered=%d",
			rep2.Frames, rep2.RecoveredFrames)
	}
	if rep2.ManifestSHA != rep.ManifestSHA {
		t.Fatal("recovered manifest must hash identically across restarts")
	}
}

func TestRunSlowConsumerBackpressure(t *testing.T) {
	cfg := baseCfg(t, filepath.Join(t.TempDir(), "slow"))
	cfg.QueueCapacity = 2
	cfg.ConsumerDelay = 5 * time.Millisecond
	rep, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Frames != 50 {
		t.Fatalf("slow consumer must still deliver every frame, got %d", rep.Frames)
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	if _, err := Run(context.Background(), Config{FPS: 31, Duration: time.Second, TargetBitrate: 1}); err == nil {
		t.Fatal("fps above ceiling must be rejected")
	}
}

func TestRunMemoryCeilingAndMultiTrack(t *testing.T) {
	cfg := baseCfg(t, filepath.Join(t.TempDir(), "mem"))
	cfg.QueueCapacity = 8
	rep, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Ceiling must equal capacity*maxSample + the 1MiB fixed working buffer.
	frameBytes := 2_400_000 / 8 / 25
	wantCeiling := 8*(frameBytes+1024) + (1 << 20)
	if rep.MemoryCeiling != wantCeiling {
		t.Fatalf("memory ceiling=%d want %d", rep.MemoryCeiling, wantCeiling)
	}
	if rep.QueueCapacity != 8 {
		t.Fatalf("queue capacity=%d want 8", rep.QueueCapacity)
	}
}

func TestGenerateTracksIncludesRTPAndMetadata(t *testing.T) {
	cfg := baseCfg(t, filepath.Join(t.TempDir(), "tracks"))
	tracks, err := generateTracks(cfg, 50, 12000)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, tr := range tracks {
		kinds[string(tr.Kind)] = true
	}
	if !kinds["video"] {
		t.Fatal("expected MP4 and RTP video tracks")
	}
	// At least three distinct tracks: mp4 video, metadata groups, RTP video.
	if len(tracks) < 3 {
		t.Fatalf("expected multi-track timeline, got %d tracks", len(tracks))
	}
}
