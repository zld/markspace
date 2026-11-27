package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// runMain invokes run() with explicit args and captures nothing itself; the
// JSON envelope goes to stdout which tests read by redirecting via a helper.
func invoke(t *testing.T, args ...string) int {
	t.Helper()
	return run(args)
}

func TestDemoHealthyJSON(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "txn")
	code := invoke(t, "demo", "--codec", "h264",
		"--chunk-bytes", "512", "--queue", "2",
		"--faults", "none", "--out", dir, "--json")
	if code != 0 {
		t.Fatalf("exit code=%d want 0", code)
	}
}

func TestDemoInterruptThenResume(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "txn")
	// First run interrupted after scan.
	if code := invoke(t, "demo", "--codec", "all",
		"--chunk-bytes", "4096", "--queue", "2",
		"--faults", "truncated,invalid-length",
		"--interrupt-after-stage", "scan",
		"--out", dir, "--json"); code != 0 {
		t.Fatalf("interrupted run exit=%d want 0", code)
	}
	// Resume must complete and mark recovered.
	if code := invoke(t, "demo", "--codec", "all",
		"--chunk-bytes", "4096", "--queue", "2",
		"--faults", "truncated,invalid-length",
		"--resume", "--out", dir, "--json"); code != 0 {
		t.Fatalf("resume exit=%d want 0", code)
	}
}

func TestEnvelopeJSONIsWellFormed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "txn")
	// Redirect stdout to capture the JSON envelope.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code := run([]string{"demo", "--codec", "h265", "--out", dir,
		"--chunk-bytes", "256", "--queue", "2", "--json"})
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	var env envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON envelope: %v\n%s", err, buf.String())
	}
	if !env.OK || env.RoundtripConsistency != 1.0 {
		t.Fatalf("envelope contract wrong: %+v", env)
	}
	if env.SegmentSHA256Count <= 0 {
		t.Fatal("segment hash count must be positive")
	}
}

func TestBadCodecExitCode(t *testing.T) {
	if code := invoke(t, "demo", "--codec", "vp9",
		"--out", t.TempDir()); code != 2 {
		t.Fatalf("bad codec exit=%d want 2", code)
	}
}
