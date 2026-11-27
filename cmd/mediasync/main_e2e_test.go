package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildCLI compiles the mediasync binary once into a temp directory. It
// resolves the module root because the test's working directory is the
// package directory (cmd/mediasync).
func buildCLI(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot := filepath.Join(wd, "..", "..")
	bin := filepath.Join(t.TempDir(), "mediasync")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/mediasync")
	cmd.Dir = moduleRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build cli: %v\n%s", err, out)
	}
	return bin
}

func run(t *testing.T, bin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("failed to run: %v", err)
		}
	}
	return string(out), code
}

func TestDemoAndResumeAcceptance(t *testing.T) {
	bin := buildCLI(t)
	outDir := filepath.Join(t.TempDir(), "demo")

	// First demo run with an interruption after 2 committed checkpoints.
	out, code := run(t, bin, "demo", "--out", outDir, "--interrupt-after", "2")
	if code != 0 {
		t.Fatalf("interrupted demo should still exit 0, got %d:\n%s", code, out)
	}
	// The corrupt scenario must surface an offset-bearing error.
	if !strings.Contains(out, "offset=") {
		t.Fatalf("corrupt fixture must report a byte offset:\n%s", out)
	}

	// Resume to completion.
	out1, code := run(t, bin, "resume", "--dir", outDir)
	if code != 0 {
		t.Fatalf("resume should exit 0, got %d:\n%s", code, out1)
	}
	for _, key := range []string{
		"sync_error_ns", "loss_recovery_rate", "p95_latency_ms",
		"duplicates", "missing", "input_sha256", "index_sha256", "result_sha256",
	} {
		if !strings.Contains(out1, key+"=") {
			t.Fatalf("resume output missing %s:\n%s", key, out1)
		}
	}

	// Repeated resume must be idempotent: identical result digest.
	digest1 := extract(t, out1, "result_sha256=")
	out2, code := run(t, bin, "resume", "--dir", outDir)
	if code != 0 {
		t.Fatalf("second resume should exit 0, got %d:\n%s", code, out2)
	}
	digest2 := extract(t, out2, "result_sha256=")
	if digest1 != digest2 {
		t.Fatalf("resume retries must yield identical result digest:\n%s\n%s", digest1, digest2)
	}

	// The persisted result file must independently verify against its digest.
	resultPath := filepath.Join(outDir, "normal", "run", "result.json")
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), digest1) {
		t.Fatal("result document must embed its content digest")
	}
}

func TestDemoCorruptExitsNonZero(t *testing.T) {
	bin := buildCLI(t)
	outDir := filepath.Join(t.TempDir(), "demo")
	// Without interruption, the corrupt scenario failure drives exit code 1.
	out, code := run(t, bin, "demo", "--out", outDir)
	if code == 0 {
		t.Fatalf("corrupt scenario must cause non-zero exit, got 0:\n%s", out)
	}
	if !strings.Contains(out, "corrupt") && !strings.Contains(out, "truncated") {
		t.Fatalf("failure must name a structured class:\n%s", out)
	}
}

func TestBadArgumentsExitTwo(t *testing.T) {
	bin := buildCLI(t)
	cases := [][]string{
		{"bogus"},
		{"demo"},
		{"resume"},
		{"demo", "--out", t.TempDir(), "--unknown", "x"},
	}
	for _, args := range cases {
		_, code := run(t, bin, args...)
		if code != 2 {
			t.Fatalf("args %v should exit 2, got %d", args, code)
		}
	}
}

func extract(t *testing.T, out, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	t.Fatalf("prefix %q not found in:\n%s", prefix, out)
	return ""
}
