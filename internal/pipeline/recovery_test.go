package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResumeCompleteMatchesCold(t *testing.T) {
	gt := groundTruth(t)
	cp := filepath.Join(t.TempDir(), "recover")
	cold := runScan(t, gt.CorruptManifest, cp, false, 0)

	res, err := Run(context.Background(), Config{
		Input: gt.CorruptManifest, CheckpointPath: cp, Resume: true, MemoryMiB: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 2 {
		t.Fatalf("exit=%d", res.ExitCode)
	}
	if res.ResumeFromSample != cold.Report.TotalSamples {
		t.Fatalf("resume_from_sample=%d want %d", res.ResumeFromSample, cold.Report.TotalSamples)
	}
	if res.Report.SHA256 != cold.Report.SHA256 {
		t.Fatalf("resume sha %s != cold %s", res.Report.SHA256, cold.Report.SHA256)
	}
}

func TestInterruptCommitsBoundaryThenResume(t *testing.T) {
	gt := groundTruth(t)
	cp := filepath.Join(t.TempDir(), "cp")

	_, err := Run(context.Background(), Config{
		Input: gt.CorruptManifest, CheckpointPath: cp,
		MemoryMiB: 8, InterruptAfterSegments: 1,
	})
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
	if _, err := os.Stat(cp); err != nil {
		t.Fatalf("checkpoint not committed at boundary: %v", err)
	}

	res, err := Run(context.Background(), Config{
		Input: gt.CorruptManifest, CheckpointPath: cp, Resume: true, MemoryMiB: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	// First corrupt segment has 512 samples and is the committed boundary.
	if res.ResumeFromSample != 512 {
		t.Fatalf("resume_from_sample=%d want 512", res.ResumeFromSample)
	}

	full := runScan(t, gt.CorruptManifest, filepath.Join(t.TempDir(), "full"), false, 0)
	if res.Report.SHA256 != full.Report.SHA256 {
		t.Fatalf("resumed sha %s != uninterrupted %s", res.Report.SHA256, full.Report.SHA256)
	}
}

func TestCorruptCheckpointRollsToSafeBoundary(t *testing.T) {
	gt := groundTruth(t)
	cp := filepath.Join(t.TempDir(), "cp")
	cold := runScan(t, gt.CorruptManifest, cp, false, 0)

	if err := os.WriteFile(cp, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cp+".bak", []byte("{also broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), Config{
		Input: gt.CorruptManifest, CheckpointPath: cp, Resume: true, MemoryMiB: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RollbackReason == "" || !strings.Contains(res.RollbackReason, "rolled back") {
		t.Fatalf("expected rollback reason, got %q", res.RollbackReason)
	}
	if res.Report.SHA256 != cold.Report.SHA256 {
		t.Fatal("rolled-back run must reproduce the same checksum")
	}
}

func TestPathEscapeIsUnrecoverable(t *testing.T) {
	dir := t.TempDir()
	pl := "#EXTM3U\n#EXTINF:1.0,\n../../../../../../etc/passwd\n"
	if err := os.WriteFile(filepath.Join(dir, "evil.m3u8"), []byte(pl), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), Config{
		Input: filepath.Join(dir, "evil.m3u8"), MemoryMiB: 8,
	})
	if err == nil {
		t.Fatal("expected path escape to fail")
	}
}
