package engine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mediapipe/internal/fixture"
	"mediapipe/internal/model"
)

func baseConfig(dir string) Config {
	return Config{
		WorkDir: dir, ColorTarget: "bt709", TargetRate: 48000,
		TargetLUFS: -23, TruePeak: 1.0,
	}
}

func buildAndAttach(t *testing.T, dir string, spec fixture.Spec) *fixture.Generated {
	t.Helper()
	gen, err := fixture.Build(dir, spec)
	if err != nil {
		t.Fatal(err)
	}
	return gen
}

func TestHealthyRunDeterministic(t *testing.T) {
	dir := t.TempDir()
	spec := fixture.Spec{Seed: 17, Frames: 60, AudioRate: 44100, Channels: 6}
	gen := buildAndAttach(t, dir, spec)

	e1, err := New(baseConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	e1.AttachPCM(gen.AudioTrack.PCM)
	s1, err := e1.Run(time.Now())
	if err != nil {
		t.Fatalf("healthy run failed: %v", err)
	}
	if s1.TrackCount != 2 || s1.FrameCount != 60 || s1.CorruptFirstFrame != -1 {
		t.Fatalf("unexpected summary: %+v", s1)
	}
	if s1.ClipCount < 0 || s1.LoudnessLUFS == silentLUFS {
		t.Fatalf("audio metrics missing: %+v", s1)
	}

	// A second process over the same checkpoint skips every stage and must
	// reproduce the identical output hash and metrics.
	e2, err := New(baseConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	e2.AttachPCM(gen.AudioTrack.PCM)
	s2, err := e2.Run(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s2.OutputSHA256 != s1.OutputSHA256 {
		t.Fatalf("rerun hash %s != %s", s2.OutputSHA256, s1.OutputSHA256)
	}
	if s2.ReusedStages == 0 {
		t.Fatal("second run must reuse cached stages")
	}
	if s2.LoudnessLUFS != s1.LoudnessLUFS || s2.ClipCount != s1.ClipCount ||
		s2.FrameCount != s1.FrameCount {
		t.Fatalf("summary metrics drifted across reruns: %+v vs %+v", s1, s2)
	}
}

func TestInterruptAndResume(t *testing.T) {
	dir := t.TempDir()
	spec := fixture.Spec{Seed: 17, Frames: 40, AudioRate: 48000, Channels: 2}
	gen := buildAndAttach(t, dir, spec)

	cfg := baseConfig(dir)
	cfg.InterruptAfter = 2
	e1, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e1.AttachPCM(gen.AudioTrack.PCM)
	if _, err := e1.Run(time.Now()); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("got %v, want ErrInterrupted", err)
	}

	// Simulate a fresh process: new engine, checkpoint on disk, no interrupt.
	resumeCfg := baseConfig(dir)
	e2, err := New(resumeCfg)
	if err != nil {
		t.Fatal(err)
	}
	e2.AttachPCM(gen.AudioTrack.PCM)
	started := time.Now()
	s2, err := e2.Run(started)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if s2.ReusedStages < 2 {
		t.Fatalf("resume reused %d stages, want >= 2", s2.ReusedStages)
	}
	if s2.ResumeMS >= 5000 {
		t.Fatalf("resume took %d ms, want < 5000", s2.ResumeMS)
	}

	// A third uninterrupted process from the completed checkpoint must hash
	// identically to the resumed completion.
	e3, err := New(baseConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	e3.AttachPCM(gen.AudioTrack.PCM)
	s3, err := e3.Run(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s3.OutputSHA256 != s2.OutputSHA256 {
		t.Fatalf("resumed hash %s != final hash %s", s2.OutputSHA256, s3.OutputSHA256)
	}
}

func TestCorruptInputBlocksDependentsAndReportsLocation(t *testing.T) {
	dir := t.TempDir()
	spec := fixture.Spec{Seed: 17, Frames: 30, AudioRate: 48000, Channels: 2,
		Corruption: fixture.Truncated}
	gen := buildAndAttach(t, dir, spec)

	e, err := New(baseConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	e.AttachPCM(gen.AudioTrack.PCM)
	s, err := e.Run(time.Now())
	me := model.AsMediaError(err)
	if me == nil || me.ExitCode() != 2 {
		t.Fatalf("corruption must exit-classify 2, got %v", err)
	}
	if s.CorruptFirstFrame != gen.CorruptAt {
		t.Fatalf("located frame %d, want %d", s.CorruptFirstFrame, gen.CorruptAt)
	}
	if s.CorruptOffset <= 0 {
		t.Fatalf("byte offset not reported: %d", s.CorruptOffset)
	}
	// Video color and cut both depend on the failed verify stage.
	blocked := map[string]bool{}
	for _, b := range s.BlockedStages {
		blocked[b] = true
	}
	if !blocked["vid-1/color"] || !blocked["vid-1/cut"] {
		t.Fatalf("downstream not blocked: %v", s.BlockedStages)
	}
}

func TestUnknownColorBlocksDownstreamOnly(t *testing.T) {
	dir := t.TempDir()
	spec := fixture.Spec{Seed: 17, Frames: 30, AudioRate: 48000, Channels: 2,
		Corruption: fixture.UnknownColor}
	gen := buildAndAttach(t, dir, spec)

	e, err := New(baseConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	e.AttachPCM(gen.AudioTrack.PCM)
	s, err := e.Run(time.Now())
	me := model.AsMediaError(err)
	if me == nil || me.Type != model.ErrUnknownColor {
		t.Fatalf("got %v, want unknown_color_label", err)
	}
	// Verification passes; only the color chain is blocked.
	if s.CorruptFirstFrame != -1 {
		t.Fatalf("unknown color is not payload corruption: %+v", s)
	}
	blocked := map[string]bool{}
	for _, b := range s.BlockedStages {
		blocked[b] = true
	}
	if !blocked["vid-1/cut"] {
		t.Fatalf("cut stage must be blocked, got %v", s.BlockedStages)
	}
	if blocked["aud-1/loudness"] {
		t.Fatal("independent audio chain must keep running")
	}
	if s.ClipCount < 0 || s.LoudnessLUFS == 0 {
		t.Fatal("audio metrics should still be produced on the independent chain")
	}
}

func TestCheckpointFileIsAtStablePath(t *testing.T) {
	dir := t.TempDir()
	gen := buildAndAttach(t, dir, fixture.Spec{Seed: 1, Frames: 12, AudioRate: 48000, Channels: 1})
	e, err := New(baseConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	e.AttachPCM(gen.AudioTrack.PCM)
	if _, err := e.Run(time.Now()); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(dir, "checkpoint.json")) {
		t.Fatal("checkpoint.json must be committed to the work dir")
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
