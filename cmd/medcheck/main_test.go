package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMedcheckCommandEndToEnd builds the real CLI and verifies the acceptance
// output lines and a zero exit code, exactly as the spec's terminal run does.
func TestMedcheckCommandEndToEnd(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "medcheck")
	build := exec.Command("go", "build", "-o", bin, "./cmd/medcheck")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "--case", "all", "--late-window", "3", "--emit-hashes")
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		t.Fatalf("medcheck exited non-zero: %v\n%s", err, text)
	}

	required := []string{
		"parse_roundtrip=1.000",
		"keyframe_hit=1.000",
		"offset_trace=1.000",
		"replay_equal=true",
		"all_assertions_passed=true",
	}
	for _, line := range required {
		if !strings.Contains(text, line) {
			t.Errorf("output missing %q\nfull output:\n%s", line, text)
		}
	}
	if !strings.Contains(text, "segment_sha256_count=") ||
		strings.Contains(text, "segment_sha256_count=0") {
		t.Errorf("segment_sha256_count must be >=1:\n%s", text)
	}
	if !strings.Contains(text, "shard_sha256=") {
		t.Errorf("--emit-hashes must list shard hashes:\n%s", text)
	}
	// Summary hashes from the two arrival orders must be byte-identical.
	var fwd, rev string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "summary_hash_forward=") {
			fwd = strings.TrimPrefix(line, "summary_hash_forward=")
		}
		if strings.HasPrefix(line, "summary_hash_reverse=") {
			rev = strings.TrimPrefix(line, "summary_hash_reverse=")
		}
	}
	if fwd == "" || fwd != rev {
		t.Errorf("forward/reverse summary hashes differ: %q vs %q", fwd, rev)
	}
}

// TestMedcheckRejectsBadArgs confirms invalid parameters fail non-zero.
func TestMedcheckRejectsBadArgs(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "medcheck")
	build := exec.Command("go", "build", "-o", bin, "./cmd/medcheck")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	if err := exec.Command(bin, "--case", "nope").Run(); err == nil {
		t.Fatal("invalid --case must exit non-zero")
	}
}

// repoRoot walks up to the directory containing go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			t.Fatal("go.mod not found")
		}
		wd = parent
	}
}
