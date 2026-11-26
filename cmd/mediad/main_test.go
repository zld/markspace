package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildCLI compiles the mediad binary once into the test's temp directory.
func buildCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "mediad")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return binary
}

func TestCLIDemoSuccess(t *testing.T) {
	binary := buildCLI(t)
	root := filepath.Join(t.TempDir(), "demo-root")
	cmd := exec.Command(binary, "demo", "--root", root,
		"--seed", "23", "--max-bitrate", "1500000", "--budget-units", "900000000")
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		t.Fatalf("demo exited non-zero: %v\n%s", err, text)
	}
	required := []string{
		"tracks=2", "samples=8", "selected=960x540", "bitrate=1500000",
		"quality=0.25", "coverage=1", "resumedFromSample=4",
		"duplicateWrites=0", "outputDiff=0", "demo=PASS",
	}
	for _, want := range required {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q\nfull:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "manifestDigest=") {
		t.Fatal("manifest digest missing")
	}
	for _, bad := range []string{"truncated-mp4", "zero-timescale", "oob-transition"} {
		if !strings.Contains(text, bad) {
			t.Fatalf("corrupt case %q not reported", bad)
		}
	}
}

func TestCLIDemoFailsOnTinyBitrate(t *testing.T) {
	binary := buildCLI(t)
	root := filepath.Join(t.TempDir(), "demo-root")
	// A bitrate below every candidate leaves no feasible configuration.
	cmd := exec.Command(binary, "demo", "--root", root,
		"--seed", "23", "--max-bitrate", "1", "--budget-units", "900000000")
	var ee *exec.ExitError
	err := cmd.Run()
	if !errors.As(err, &ee) {
		t.Fatalf("want ExitError, got %v", err)
	}
	if ee.ExitCode() != 2 {
		t.Fatalf("infeasible constraints exit = %d, want 2", ee.ExitCode())
	}
}

func TestCLIMissingRoot(t *testing.T) {
	binary := buildCLI(t)
	cmd := exec.Command(binary, "demo")
	if err := cmd.Run(); err == nil {
		t.Fatal("missing --root must fail")
	}
}
