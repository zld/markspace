package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI invokes the command in-process and returns its exit code and output.
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestDemoPassesTwiceAndIsStable(t *testing.T) {
	dir := t.TempDir()
	args := []string{"demo", "--out", dir, "--chunk", "4096", "--queue", "2"}
	code, out1, err1 := runCLI(t, args...)
	if code != 0 {
		t.Fatalf("first demo exit=%d\nstdout:\n%s\nstderr:\n%s", code, out1, err1)
	}
	for _, want := range []string{
		"== stage status", "cancellation reason:", "resume byte offset:",
		"damaged offset=", "keyframes=", "intervals=",
		"repeat run: added=0", "result: OK - archive verified",
		"manifest checksum is stable across runs",
	} {
		if !strings.Contains(out1, want) {
			t.Errorf("demo output missing %q", want)
		}
	}
	if strings.Contains(out1, "[FAIL]") {
		t.Errorf("demo reported a failed assertion:\n%s", out1)
	}

	code, out2, err2 := runCLI(t, args...)
	if code != 0 {
		t.Fatalf("second demo exit=%d\nstdout:\n%s\nstderr:\n%s", code, out2, err2)
	}
	if !strings.Contains(out2, "previous=") {
		t.Error("second run should compare against the recorded checksum")
	}
	checksum := func(s string) string {
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "checksum: ") {
				return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "checksum: "))
			}
		}
		return ""
	}
	if c1, c2 := checksum(out1), checksum(out2); c1 == "" || c1 != c2 {
		t.Errorf("manifest checksums differ: %q vs %q", c1, c2)
	}
}

func TestVerifyDetectsTamperedDerivative(t *testing.T) {
	dir := t.TempDir()
	if code, _, stderr := runCLI(t, "demo", "--out", dir, "--chunk", "4096", "--queue", "2"); code != 0 {
		t.Fatalf("demo failed: %s", stderr)
	}
	if code, out, _ := runCLI(t, "verify", "--out", dir); code != 0 || !strings.Contains(out, "result: OK") {
		t.Fatalf("verify failed: %d %s", code, out)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "derivatives", "*", "*", "*"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no derivatives: %v %v", matches, err)
	}
	if err := os.WriteFile(matches[0], []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI(t, "verify", "--out", dir); code == 0 {
		t.Errorf("tampered archive verified: %s", stderr)
	}
}

func TestDemoRejectsBadArguments(t *testing.T) {
	if code, _, _ := runCLI(t, "demo"); code != exitUsage {
		t.Errorf("missing --out exit = %d, want %d", code, exitUsage)
	}
	if code, _, _ := runCLI(t, "demo", "--out", t.TempDir(), "--chunk", "0"); code != exitFail {
		t.Errorf("zero chunk exit = %d, want %d", code, exitFail)
	}
	if code, _, _ := runCLI(t, "demo", "--out", t.TempDir(), "--queue", "-1"); code != exitFail {
		t.Errorf("negative queue exit = %d, want %d", code, exitFail)
	}
	if code, _, _ := runCLI(t, "bogus"); code != exitUsage {
		t.Errorf("unknown command exit = %d, want %d", code, exitUsage)
	}
	if code, _, _ := runCLI(t); code != exitUsage {
		t.Errorf("no arguments exit = %d, want %d", code, exitUsage)
	}
}

func TestDemoOutputRefusesToBeConfusedByASourceChange(t *testing.T) {
	dir := t.TempDir()
	args := []string{"demo", "--out", dir, "--chunk", "4096", "--queue", "2"}
	if code, _, stderr := runCLI(t, args...); code != 0 {
		t.Fatalf("demo failed: %s", stderr)
	}
	// Corrupt a generated sample between runs; the digest changes, so any
	// checkpoint for it must be dropped rather than trusted.
	sample := filepath.Join(dir, "samples", "subs-valid.vtt")
	if err := os.WriteFile(sample, []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runCLI(t, args...)
	if code != 0 {
		t.Fatalf("demo with changed sample exit=%d\n%s\n%s", code, out, stderr)
	}
	// The demo regenerates its samples, restoring the original digest.
	if !strings.Contains(out, "result: OK") {
		t.Errorf("demo did not recover: %s", out)
	}
}
