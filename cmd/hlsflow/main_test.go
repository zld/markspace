package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildBinary compiles the CLI once per package test run.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "hlsflow")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build cli: %v", err)
	}
	return bin
}

func runCLI(t *testing.T, bin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return string(out), code
}

func TestCLIFixtureBuildVerifyFaults(t *testing.T) {
	bin := buildBinary(t)
	dir := filepath.Join(t.TempDir(), "demo")
	if out, code := runCLI(t, bin, "fixture", "--dir", dir,
		"--scenario", "faults", "--rates", "800000,1800000,3500000"); code != 0 {
		t.Fatalf("fixture exit %d: %s", code, out)
	}
	if out, code := runCLI(t, bin, "build",
		"--job", filepath.Join(dir, "job.json"),
		"--input", filepath.Join(dir, "inbox"),
		"--output", filepath.Join(dir, "out"),
		"--target-duration", "2s", "--gap-timeout", "3s", "--finalize"); code != 0 {
		t.Fatalf("build exit %d: %s", code, out)
	} else if !strings.Contains(out, "rendition=720p") {
		t.Fatalf("build output missing rendition line:\n%s", out)
	}
	// Verification must fail (exit 1) because of the injected checksum fault.
	out, code := runCLI(t, bin, "verify",
		"--job", filepath.Join(dir, "job.json"),
		"--master", filepath.Join(dir, "out", "master.m3u8"), "--json")
	if code != 1 {
		t.Fatalf("verify exit = %d, want 1; output:\n%s", code, out)
	}
	if !strings.Contains(out, "\"checksum_failures\": 1") {
		t.Fatalf("json missing checksum failure:\n%s", out)
	}
	if !strings.Contains(out, "sha256 mismatch") {
		t.Fatalf("json missing failure reason:\n%s", out)
	}
}

func TestCLICleanTreeVerifiesOK(t *testing.T) {
	bin := buildBinary(t)
	dir := filepath.Join(t.TempDir(), "clean")
	if _, code := runCLI(t, bin, "fixture", "--dir", dir); code != 0 {
		t.Fatal("fixture failed")
	}
	if _, code := runCLI(t, bin, "build",
		"--job", filepath.Join(dir, "job.json"),
		"--input", filepath.Join(dir, "inbox"),
		"--output", filepath.Join(dir, "out"), "--finalize"); code != 0 {
		t.Fatal("build failed")
	}
	out, code := runCLI(t, bin, "verify",
		"--job", filepath.Join(dir, "job.json"),
		"--master", filepath.Join(dir, "out", "master.m3u8"))
	if code != 0 {
		t.Fatalf("clean verify exit %d: %s", code, out)
	}
}

func TestCLIUsageErrors(t *testing.T) {
	bin := buildBinary(t)
	cases := [][]string{
		{"bogus"},
		{"fixture"},
		{"build", "--job", "x"},
		{"verify"},
	}
	for _, args := range cases {
		if _, code := runCLI(t, bin, args...); code != 2 {
			t.Fatalf("args %v exit = %d, want 2", args, code)
		}
	}
}
