package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestUsageErrors(t *testing.T) {
	cases := [][]string{
		{},                                    // no command
		{"bogus"},                             // unknown command
		{"demo", "--not-a-flag"},              // unknown flag
		{"demo", "--loss", "1.5"},             // loss out of range
		{"demo", "--chunk-size", "0"},         // chunk size must be positive
		{"demo", "--stop-after-chunks", "-1"}, // negative stop
		{"demo", "extra"},                     // stray positional
	}
	for _, args := range cases {
		code, _, _ := runCLI(t, args...)
		if code != exitUsage {
			t.Errorf("args %v: code = %d, want %d", args, code, exitUsage)
		}
	}
}

func TestDemoAcceptanceFigures(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := runCLI(t, "demo", "--seed", "7", "--loss", "0.04",
		"--chunk-size", "64", "--report", "stdout", "--work-dir", dir)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	metrics := parseMetrics(out)
	// Acceptance bounds from the brief.
	if got := metrics["av_sync_ms"]; got > 40 {
		t.Fatalf("av_sync_ms = %v > 40", got)
	}
	if got := metrics["loss_recovery_rate"]; got < 0.95 {
		t.Fatalf("loss_recovery_rate = %v < 0.95", got)
	}
	if got := metrics["latency_p95_ms"]; got > 100 {
		t.Fatalf("latency_p95_ms = %v > 100", got)
	}
	if metrics["aligned_segments"] == 0 {
		t.Fatal("no aligned segments")
	}
	if !strings.Contains(out, "manifest_sha256=") {
		t.Fatal("manifest_sha256 missing")
	}
	if !strings.Contains(out, "replacement_index_reused=false") {
		t.Fatal("same-path replacement must defeat index reuse")
	}
	if !strings.Contains(out, "damaged_rejected=true") {
		t.Fatal("damaged inputs must be rejected")
	}
	if !strings.Contains(out, "damaged_issue: error[") {
		t.Fatal("damaged input must be reported with a located issue")
	}
}

func TestDemoResumeMatchesContinuous(t *testing.T) {
	continuousDir := t.TempDir()
	_, contOut, _ := runCLI(t, "demo", "--seed", "7", "--loss", "0.04", "--chunk-size", "64", "--work-dir", continuousDir)
	contDigest := digestOf(t, contOut)

	resumeDir := t.TempDir()
	code, _, _ := runCLI(t, "demo", "--seed", "7", "--loss", "0.04", "--chunk-size", "64",
		"--stop-after-chunks", "2", "--work-dir", resumeDir)
	if code != exitOK {
		t.Fatalf("stop run exit = %d", code)
	}
	_, resumeOut, _ := runCLI(t, "demo", "--seed", "7", "--loss", "0.04", "--chunk-size", "64", "--work-dir", resumeDir)
	resumeDigest := digestOf(t, resumeOut)
	if contDigest != resumeDigest {
		t.Fatalf("resumed digest %s != continuous %s", resumeDigest, contDigest)
	}
}

func TestDemoStopAfterIsPartial(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := runCLI(t, "demo", "--seed", "7", "--loss", "0.04", "--chunk-size", "64",
		"--stop-after-chunks", "2", "--work-dir", dir)
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "partial:") {
		t.Fatalf("expected a partial line, got %q", out)
	}
	if strings.Contains(out, "manifest_sha256=") {
		t.Fatal("a partial run must not publish a manifest")
	}
}

func parseMetrics(out string) map[string]float64 {
	m := map[string]float64{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		var f float64
		if _, err := fmt.Sscan(v, &f); err == nil {
			m[k] = f
		}
	}
	return m
}

func digestOf(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "manifest_sha256=") {
			return strings.TrimPrefix(line, "manifest_sha256=")
		}
	}
	t.Fatalf("no manifest digest in output:\n%s", out)
	return ""
}
