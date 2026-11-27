package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI invokes run with temporary stdout/stderr buffers and returns the
// exit code and captured text.
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	outFile, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer outFile.Close()
	errFile, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer errFile.Close()

	code := run(args, outFile, errFile)
	outBytes, _ := os.ReadFile(outFile.Name())
	errBytes, _ := os.ReadFile(errFile.Name())
	return code, string(outBytes), string(errBytes)
}

func TestNormalDemoSucceeds(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := runCLI(t, "demo", "--case", "normal", "--versions", "v1,v2", "--workdir", dir)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	for _, want := range []string{
		"tracks: 2", "packets:", "lost:", "missing:", "conflicts: 0",
		"manifest sha256:", "manifest verified: true",
		"pending -> running", "running -> completed",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	// The manifest and plan must exist on disk.
	for _, name := range []string{"manifest.json", "out/plan.json"} {
		if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); statErr != nil {
			t.Fatalf("%s missing: %v", name, statErr)
		}
	}
}

func TestNormalDemoRepeatIsStable(t *testing.T) {
	dir := t.TempDir()
	if code, _, errOut := runCLI(t, "demo", "--case", "normal", "--workdir", dir); code != 0 {
		t.Fatalf("first run exit=%d %s", code, errOut)
	}
	code, out, errOut := runCLI(t, "demo", "--case", "normal", "--workdir", dir)
	if code != 0 {
		t.Fatalf("second run exit=%d %s", code, errOut)
	}
	if !strings.Contains(out, "diff vs previous run: 0 change(s)") {
		t.Fatalf("repeat run must report zero diff:\n%s", out)
	}
}

func TestTruncatedRTCPFailsWithOffset(t *testing.T) {
	code, out, errOut := runCLI(t, "demo", "--case", "truncated-rtcp", "--workdir", t.TempDir())
	if code != 1 {
		t.Fatalf("exit=%d want 1", code)
	}
	if !strings.Contains(out, "byte offset:") {
		t.Fatalf("missing offset report:\n%s", out)
	}
	if !strings.Contains(errOut, "rtcp packet truncated") {
		t.Fatalf("missing truncation reason: %s", errOut)
	}
}

func TestConflictCaseReportsConflict(t *testing.T) {
	code, out, errOut := runCLI(t, "demo", "--case", "duplicate-conflict", "--workdir", t.TempDir())
	if code != 0 {
		t.Fatalf("exit=%d %s", code, errOut)
	}
	if !strings.Contains(out, "conflicts: 1") && !strings.Contains(out, "conflicts: 2") {
		t.Fatalf("expected at least one conflict:\n%s", out)
	}
}

func TestResumeStopAndContinue(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := runCLI(t, "demo", "--case", "resume", "--stop-after", "map", "--workdir", dir)
	if code != 0 {
		t.Fatalf("stop run exit=%d %s", code, errOut)
	}
	if !strings.Contains(out, "status: stopped after map") {
		t.Fatalf("missing stop status:\n%s", out)
	}
	if !strings.Contains(out, "resume position: 50 packets") {
		t.Fatalf("resume position should equal processed packets:\n%s", out)
	}

	code, out, errOut = runCLI(t, "demo", "--case", "resume", "--resume", "--workdir", dir)
	if code != 0 {
		t.Fatalf("resume exit=%d %s", code, errOut)
	}
	if !strings.Contains(out, "manifest verified: true") {
		t.Fatalf("resume did not verify manifest:\n%s", out)
	}

	code, out, errOut = runCLI(t, "demo", "--case", "resume", "--resume", "--workdir", dir)
	if code != 0 {
		t.Fatalf("second resume exit=%d %s", code, errOut)
	}
	if !strings.Contains(out, "diff vs previous run: 0 change(s)") {
		t.Fatalf("repeat resume must be a no-op:\n%s", out)
	}
}

func TestDependencyCycleFailsAtGraph(t *testing.T) {
	code, out, _ := runCLI(t, "demo", "--case", "dependency-cycle", "--workdir", t.TempDir())
	if code != 1 {
		t.Fatalf("exit=%d want 1", code)
	}
	if !strings.Contains(out, "failed stage: graph") {
		t.Fatalf("expected graph failure:\n%s", out)
	}
}

func TestUnknownCaseIsArgumentError(t *testing.T) {
	code, _, errOut := runCLI(t, "demo", "--case", "nope", "--workdir", t.TempDir())
	if code != 2 {
		t.Fatalf("exit=%d want 2", code)
	}
	if !strings.Contains(errOut, "unknown scenario case") {
		t.Fatalf("stderr=%s", errOut)
	}
}

func TestBadFlagsAreArgumentErrors(t *testing.T) {
	cases := [][]string{
		{"demo", "--nope"},
		{"demo", "--stop-after", "nowhere"},
		{"demo", "--resume", "--stop-after", "map"},
		{"demo", "--versions", ""},
		{"verify"},
		{"frobnicate"},
	}
	for _, args := range cases {
		if code, _, _ := runCLI(t, args...); code != 2 {
			t.Fatalf("args %v: exit=%d want 2", args, code)
		}
	}
}

func TestVerifyCommand(t *testing.T) {
	dir := t.TempDir()
	if code, _, errOut := runCLI(t, "demo", "--case", "normal", "--workdir", dir); code != 0 {
		t.Fatalf("demo exit=%d %s", code, errOut)
	}
	code, out, errOut := runCLI(t, "verify", "--workdir", dir)
	if code != 0 {
		t.Fatalf("verify exit=%d %s", code, errOut)
	}
	if !strings.Contains(out, "manifest verified: true") {
		t.Fatalf("verify output:\n%s", out)
	}

	// Tamper with a published asset and expect verification to fail.
	asset := filepath.Join(dir, "assets", "audio-main-v1.mp4")
	data, err := os.ReadFile(asset)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(asset, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCLI(t, "verify", "--workdir", dir); code != 1 {
		t.Fatalf("tampered verify exit=%d want 1", code)
	}
}

func TestHelpAndNoArgs(t *testing.T) {
	if code, out, _ := runCLI(t, "help"); code != 0 || !strings.Contains(out, "usage: replay") {
		t.Fatalf("help exit=%d out=%s", code, out)
	}
	if code, _, _ := runCLI(t); code != 2 {
		t.Fatalf("no-args exit=%d want 2", code)
	}
}
