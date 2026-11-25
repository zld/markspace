package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	errs "mediad/internal/errors"
	"mediad/internal/fixture"
	"mediad/internal/stream"
)

func baseOpts(t *testing.T, workDir string, spec fixture.Spec) Options {
	t.Helper()
	fx, err := fixture.Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return Options{
		FixtureID: "fx-" + string(spec.Codec), Codec: spec.Codec,
		Stream: fx.Stream, IndexText: fx.IndexText, WorkDir: workDir,
		QueueSize: 2, StreamCfg: stream.DefaultConfig(),
	}
}

func TestRunExactHappyPath(t *testing.T) {
	opts := baseOpts(t, t.TempDir(), fixture.Spec{
		Codec: stream.H264, Frames: 12, IndexMode: fixture.IndexExact})
	res, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusCompleted || len(res.Segments) != 12 {
		t.Fatalf("status=%s segments=%d", res.Status, len(res.Segments))
	}
	if res.RoundTripRate != 1.0 || res.KeyframeHitRate != 1.0 {
		t.Fatalf("rates rt=%f hit=%f", res.RoundTripRate, res.KeyframeHitRate)
	}
	if res.CorruptSegmentCount != 0 || res.ManifestCanonicalHash == "" {
		t.Fatalf("corrupt=%d manifestHash=%q", res.CorruptSegmentCount, res.ManifestCanonicalHash)
	}
	if _, err := os.Stat(res.ManifestPath); err != nil {
		t.Fatalf("manifest not published: %v", err)
	}
	for _, s := range res.Segments {
		if len(s.Hash) != 64 {
			t.Fatalf("segment %d hash not SHA-256 hex: %q", s.ID, s.Hash)
		}
		if s.Silence != "undecidable" {
			t.Fatalf("video-only segment silence verdict = %q", s.Silence)
		}
	}
}

func TestRunIsReproducible(t *testing.T) {
	spec := fixture.Spec{Codec: stream.AV1, Frames: 9, IndexMode: fixture.IndexExact}
	r1, err := Run(context.Background(), baseOpts(t, t.TempDir(), spec))
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	r2, err := Run(context.Background(), baseOpts(t, t.TempDir(), spec))
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if r1.ManifestCanonicalHash != r2.ManifestCanonicalHash {
		t.Fatal("canonical manifest hash differs across identical runs")
	}
	for i := range r1.Segments {
		if r1.Segments[i].Hash != r2.Segments[i].Hash {
			t.Fatalf("segment %d hash changed on retry", i)
		}
	}
}

func TestInterruptThenResume(t *testing.T) {
	dir := t.TempDir()
	spec := fixture.Spec{Codec: stream.AV1, Frames: 12, IndexMode: fixture.IndexExact}
	opts := baseOpts(t, dir, spec)
	opts.InterruptAfter = 5

	first, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("interrupted run: %v", err)
	}
	if first.Status != StatusInterrupted {
		t.Fatalf("status = %s, want interrupted", first.Status)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("interrupted run must not publish a manifest")
	}
	cp, err := readCP(dir)
	if err != nil || cp.LastDoneAU != 5 || len(cp.CompletedAU) != 5 {
		t.Fatalf("checkpoint after interrupt = %+v err=%v", cp, err)
	}

	// Resume with the same bound input; it skips committed work and finishes.
	resumeOpts := baseOpts(t, dir, spec)
	resumeOpts.InterruptAfter = 5
	second, err := Run(context.Background(), resumeOpts)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if second.ResumedFrom != 5 || second.Status != StatusCompleted {
		t.Fatalf("resumed_from=%d status=%s", second.ResumedFrom, second.Status)
	}
	if len(second.Segments) != 12 {
		t.Fatalf("resume produced %d segments, want 12", len(second.Segments))
	}
}

func TestResumeRejectsChangedInput(t *testing.T) {
	dir := t.TempDir()
	spec := fixture.Spec{Codec: stream.H264, Frames: 6, IndexMode: fixture.IndexExact}
	opts := baseOpts(t, dir, spec)
	opts.InterruptAfter = 3
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	mutated := baseOpts(t, dir, spec)
	mutated.Stream = append(append([]byte(nil), mutated.Stream...), 0x00)
	_, err := Run(context.Background(), mutated)
	if err == nil || !errs.Is(err, errs.CodeCheckpointMismatch) {
		t.Fatalf("changed input must be refused, got %v", err)
	}
}

func TestBackpressureWithQueueOfOne(t *testing.T) {
	opts := baseOpts(t, t.TempDir(), fixture.Spec{
		Codec: stream.HEVC, Frames: 20, IndexMode: fixture.IndexExact})
	opts.QueueSize = 1
	res, err := Run(context.Background(), opts)
	if err != nil || len(res.Segments) != 20 {
		t.Fatalf("queue-size-1 run: %v segments=%d", err, len(res.Segments))
	}
}
