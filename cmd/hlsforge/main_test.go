package main

import (
	"os"
	"path/filepath"
	"testing"

	"hlsforge/internal/fixture"
)

// TestRunExitCodes exercises the real run() entry point to lock the 0/1/2
// exit-code contract end to end.
func TestRunExitCodes(t *testing.T) {
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	hls := filepath.Join(root, "hls")
	if _, err := fixture.Generate(inbox, []int{800000, 1600000}, fixture.Options{
		Seed: 7, PartMs: 500, DurationMs: 12000,
		Faults: []string{"missing", "order", "drift"},
	}); err != nil {
		t.Fatal(err)
	}

	// Unknown subcommand -> 1.
	if rc := run([]string{"bogus"}); rc != 1 {
		t.Fatalf("unknown command rc = %d, want 1", rc)
	}
	// Bad flag -> 1.
	if rc := run([]string{"build", "--segment", "nope"}); rc != 1 {
		t.Fatalf("bad flag rc = %d, want 1", rc)
	}
	// Demo with injected faults still succeeds (exit 0).
	if rc := run([]string{"demo", "--out", inbox, "--seed", "7",
		"--bitrates", "800000,1600000", "--duration", "12s",
		"--part", "500ms", "--fault", "missing,order,drift"}); rc != 0 {
		t.Fatalf("demo rc = %d, want 0", rc)
	}
	// Build succeeds even with retained gaps (exit 0).
	if rc := run([]string{"build", "--in", inbox, "--out", hls,
		"--from-seq", "1", "--to-seq", "24", "--segment", "2s",
		"--part", "500ms", "--window", "12s"}); rc != 0 {
		t.Fatalf("build rc = %d, want 0", rc)
	}
	// Verify over the faulted timeline reports consistency failures (exit 2).
	if rc := run([]string{"verify", "--in", inbox, "--hls", hls,
		"--from-seq", "1", "--to-seq", "24", "--segment", "2s",
		"--part", "500ms", "--window", "12s",
		"--duration", "12s", "--tolerance", "50ms"}); rc != 2 {
		t.Fatalf("verify rc = %d, want 2", rc)
	}
}

// TestVerifyCleanExitsZero proves a fault-free timeline verifies cleanly.
func TestVerifyCleanExitsZero(t *testing.T) {
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	hls := filepath.Join(root, "hls")
	if _, err := fixture.Generate(inbox, []int{800000}, fixture.Options{
		Seed: 1, PartMs: 500, DurationMs: 2000,
	}); err != nil {
		t.Fatal(err)
	}
	if rc := run([]string{"build", "--in", inbox, "--out", hls,
		"--from-seq", "1", "--to-seq", "4", "--segment", "2s",
		"--part", "500ms", "--window", "12s"}); rc != 0 {
		t.Fatalf("clean build rc = %d", rc)
	}
	if rc := run([]string{"verify", "--in", inbox, "--hls", hls,
		"--from-seq", "1", "--to-seq", "4", "--segment", "2s",
		"--part", "500ms", "--window", "12s",
		"--duration", "2s", "--tolerance", "50ms"}); rc != 0 {
		t.Fatalf("clean verify rc = %d, want 0", rc)
	}
}

func TestMissingDirectoryIsFileError(t *testing.T) {
	root := t.TempDir()
	// A nonexistent inbox must map to exit code 1, not panic or exit 2.
	rc := run([]string{"build", "--in", filepath.Join(root, "nope"),
		"--out", filepath.Join(root, "out"), "--from-seq", "1",
		"--to-seq", "4", "--segment", "2s", "--part", "500ms", "--window", "12s"})
	if rc != 1 {
		t.Fatalf("missing inbox rc = %d, want 1", rc)
	}
	if _, err := os.Stat(filepath.Join(root, "out")); !os.IsNotExist(err) {
		t.Fatal("no output directory should be created on ingest failure")
	}
}
