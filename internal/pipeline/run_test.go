package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediapipe/internal/model"
	"mediapipe/internal/report"
	"mediapipe/internal/sample"
	"mediapipe/internal/segment"
)

var errTestInterrupt = errors.New("test interrupt after checkpoint")

func generateTestInput(t *testing.T, dir string, frames int) {
	t.Helper()
	opts := sample.DefaultOptions(filepath.Join(dir, "input.mps"))
	opts.Frames = frames
	if err := sample.Generate(opts); err != nil {
		t.Fatal(err)
	}
}

func TestRunSummaryIsDeterministicOnRepeatedRuns(t *testing.T) {
	dir := t.TempDir()
	generateTestInput(t, dir, 260)
	opts := DefaultOptions(dir)
	opts.CheckpointPackets = 64

	first, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.Summary.Digest() != second.Manifest.Summary.Digest() {
		t.Fatalf("summary digests differ:\n%+v\n%+v", first.Manifest.Summary, second.Manifest.Summary)
	}
	v, err := report.VerifyOutput(dir)
	if err != nil || !v.OK {
		t.Fatalf("verify failed err=%v verification=%+v", err, v)
	}
}

func TestCheckpointInterruptResumesWithoutDuplicatedBlocks(t *testing.T) {
	interruptedDir := t.TempDir()
	referenceDir := t.TempDir()
	generateTestInput(t, interruptedDir, 260)
	generateTestInput(t, referenceDir, 260)

	interruptOpts := DefaultOptions(interruptedDir)
	interruptOpts.CheckpointPackets = 64
	interruptOpts.CheckpointHook = func(n int64) error {
		if n == 64 {
			return errTestInterrupt
		}
		return nil
	}
	if _, err := Run(context.Background(), interruptOpts); !errors.Is(err, errTestInterrupt) {
		t.Fatalf("interrupt err=%v, want test interrupt", err)
	}
	cp, source, err := LoadLatestCheckpoint(interruptedDir)
	if err != nil {
		t.Fatal(err)
	}
	if source != "checkpoint" || cp.ProcessedPackets != 64 {
		t.Fatalf("checkpoint source=%s processed=%d", source, cp.ProcessedPackets)
	}

	refOpts := DefaultOptions(referenceDir)
	refOpts.CheckpointPackets = 64
	reference, err := Run(context.Background(), refOpts)
	if err != nil {
		t.Fatal(err)
	}

	resumeOpts := DefaultOptions(interruptedDir)
	resumeOpts.CheckpointPackets = 64
	resumeOpts.Resume = true
	resumed, err := Run(context.Background(), resumeOpts)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ResumedFromPacket != 64 {
		t.Fatalf("resumed from %d, want 64", resumed.ResumedFromPacket)
	}

	want := reference.Manifest.Summary
	got := resumed.Manifest.Summary
	got.ResumedFromPacket = 0
	want.ResumedFromPacket = 0
	if got.Digest() != want.Digest() {
		t.Fatalf("resume summary mismatch\ngot=%+v\nwant=%+v", got, want)
	}
	if len(resumed.Manifest.Segments) != len(reference.Manifest.Segments) {
		t.Fatalf("segments %d != reference %d", len(resumed.Manifest.Segments), len(reference.Manifest.Segments))
	}
	if len(resumed.Manifest.Windows) != len(reference.Manifest.Windows) {
		t.Fatalf("windows %d != %d", len(resumed.Manifest.Windows), len(reference.Manifest.Windows))
	}
	if len(resumed.Manifest.DedupeMappings) != len(reference.Manifest.DedupeMappings) {
		t.Fatalf("mappings %d != %d", len(resumed.Manifest.DedupeMappings), len(reference.Manifest.DedupeMappings))
	}
}

func TestResumeRebuildsMissingMP4Index(t *testing.T) {
	dir := t.TempDir()
	generateTestInput(t, dir, 260)
	opts := DefaultOptions(dir)
	opts.CheckpointPackets = 64
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "index.mp4")); err != nil {
		t.Fatal(err)
	}

	resumeOpts := opts
	resumeOpts.Resume = true
	result, err := Run(context.Background(), resumeOpts)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IndexRecovered || result.ResumedFromPacket <= 0 {
		t.Fatalf("indexRecovered=%v resumed=%d", result.IndexRecovered, result.ResumedFromPacket)
	}
	if _, err := segment.ReadMP4Index(filepath.Join(dir, "index.mp4")); err != nil {
		t.Fatalf("rebuilt index unreadable: %v", err)
	}
	v, err := report.VerifyOutput(dir)
	if err != nil || !v.OK || !v.SummaryConsistent {
		t.Fatalf("verify after rebuild: err=%v v=%+v", err, v)
	}
}

func TestLoadLatestCheckpointFallsBackToPrevious(t *testing.T) {
	dir := t.TempDir()
	generateTestInput(t, dir, 220)
	opts := DefaultOptions(dir)
	opts.CheckpointPackets = 64
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "checkpoint.json"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp, source, err := LoadLatestCheckpoint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if source != "previous_checkpoint" || cp.ProcessedPackets != 128 {
		t.Fatalf("source=%s processed=%d, want previous/128", source, cp.ProcessedPackets)
	}

	resume := opts
	resume.Resume = true
	result, err := Run(context.Background(), resume)
	if err != nil {
		t.Fatal(err)
	}
	if result.ResumedFromPacket != 128 {
		t.Fatalf("resumed=%d want 128", result.ResumedFromPacket)
	}
}

func TestResumeRejectsChangedInputDigest(t *testing.T) {
	dir := t.TempDir()
	generateTestInput(t, dir, 180)
	opts := DefaultOptions(dir)
	opts.CheckpointPackets = 64
	opts.CheckpointHook = func(n int64) error {
		if n == 64 {
			return errTestInterrupt
		}
		return nil
	}
	_, _ = Run(context.Background(), opts)

	input := filepath.Join(dir, "input.mps")
	f, err := os.OpenFile(input, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("changed-input"))
	_ = f.Close()

	resumeOpts := DefaultOptions(dir)
	resumeOpts.Resume = true
	_, err = Run(context.Background(), resumeOpts)
	if !model.IsClass(err, model.ErrorDigest) {
		t.Fatalf("err=%v, want digest class", err)
	}
}
