package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// runCLI invokes the same entry point the binary uses and captures streams.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return out.String(), errb.String(), code
}

func TestDemoVerifyMigrateAcceptance(t *testing.T) {
	dir := t.TempDir()
	out, stderr, code := runCLI(t, "demo", "--seed", "17", "--out", dir)
	if code != exitOK {
		t.Fatalf("demo exit = %d, stderr = %s", code, stderr)
	}
	for _, name := range []string{"valid.mkv", "truncated.mkv", "manifest-v1.json"} {
		if !strings.Contains(out, name) {
			t.Fatalf("demo output does not mention %s:\n%s", name, out)
		}
	}

	valid := filepath.Join(dir, "valid.mkv")
	out, _, code = runCLI(t, "verify", "--input", valid)
	if code != exitOK {
		t.Fatalf("verify valid exit = %d:\n%s", code, out)
	}
	for _, want := range []string{"elements:", "tracks:", "fragments:", "max-cross-track-deviation", "sha256:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("verify output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "result:     OK") {
		t.Fatalf("valid media did not report OK:\n%s", out)
	}
	sha := shaFrom(t, out)

	// Repeated verification must yield the same hash and deviation.
	out2, _, _ := runCLI(t, "verify", "--input", valid)
	if shaFrom(t, out2) != sha {
		t.Fatalf("sha256 changed between verify runs: %s vs %s", sha, shaFrom(t, out2))
	}
	if deviationFrom(t, out2) > 1_000_000 {
		t.Fatalf("deviation above 1 ms: %d", deviationFrom(t, out2))
	}

	// Damaged input: exit 2, exact preset offset delta 0.
	trunc := filepath.Join(dir, "truncated.mkv")
	out, _, code = runCLI(t, "verify", "--input", trunc)
	if code != exitBadInput {
		t.Fatalf("truncated exit = %d, want %d:\n%s", code, exitBadInput, out)
	}
	if !strings.Contains(out, "offset-delta: 0 bytes") {
		t.Fatalf("truncated offset delta not zero:\n%s", out)
	}
	if !regexp.MustCompile(`category=truncated_element`).MatchString(out) {
		t.Fatalf("truncated category missing:\n%s", out)
	}

	// Migration with a stop, then resume, must match an uninterrupted run.
	migOut := filepath.Join(dir, "out")
	manifestPath := filepath.Join(dir, "manifest-v1.json")
	out, _, code = runCLI(t, "migrate", "--input", valid, "--manifest", manifestPath,
		"--out", migOut, "--stop-after", "normalize")
	if code != exitOK {
		t.Fatalf("stop-after migrate exit = %d:\n%s", code, out)
	}
	if !strings.Contains(out, "STAGED") {
		t.Fatalf("stop-after did not stage:\n%s", out)
	}
	out, _, code = runCLI(t, "migrate", "--input", valid, "--manifest", manifestPath,
		"--out", migOut, "--resume")
	if code != exitOK {
		t.Fatalf("resume exit = %d:\n%s", code, out)
	}
	for _, want := range []string{"resume-stage:", "elapsed-ms:", "sha256:", "reason:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("resume output missing %q:\n%s", want, out)
		}
	}
	resumedSHA := shaFrom(t, out)

	fullOut := filepath.Join(dir, "out-full")
	out, _, code = runCLI(t, "migrate", "--input", valid, "--manifest", manifestPath,
		"--out", fullOut)
	if code != exitOK {
		t.Fatalf("full migrate exit = %d:\n%s", code, out)
	}
	if shaFrom(t, out) != resumedSHA {
		t.Fatalf("resumed hash %s != uninterrupted %s", resumedSHA, shaFrom(t, out))
	}
}

func TestMigrateWithUnknownVersionExitsBadInput(t *testing.T) {
	dir := t.TempDir()
	if _, _, code := runCLI(t, "demo", "--seed", "17", "--out", dir); code != exitOK {
		t.Fatal("demo failed")
	}
	bad := filepath.Join(dir, "manifest-bad.json")
	if err := os.WriteFile(bad, []byte(`{"version": 9, "asset_id": "x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCLI(t, "migrate", "--input", filepath.Join(dir, "valid.mkv"),
		"--manifest", bad, "--out", filepath.Join(dir, "out"))
	if code != exitBadInput {
		t.Fatalf("exit = %d, want %d (stderr %s)", code, exitBadInput, stderr)
	}
	if !strings.Contains(stderr, "manifest_version") {
		t.Fatalf("stderr does not name the version category: %s", stderr)
	}
}

func TestVerifyMissingFileExitsLocalIO(t *testing.T) {
	_, stderr, code := runCLI(t, "verify", "--input", filepath.Join(t.TempDir(), "absent.mkv"))
	if code != exitLocalIO {
		t.Fatalf("exit = %d, want %d (stderr %s)", code, exitLocalIO, stderr)
	}
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	_, _, code := runCLI(t, "frobnicate")
	if code != exitUsage {
		t.Fatalf("exit = %d", code)
	}
}

func TestVerifyRequiresInput(t *testing.T) {
	_, stderr, code := runCLI(t, "verify")
	if code != exitBadInput {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stderr, "verify requires --input") {
		t.Fatalf("stderr = %s", stderr)
	}
}

func TestInvalidStopAfterRejected(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := runCLI(t, "migrate", "--input", "a", "--manifest", "b", "--out", dir,
		"--stop-after", "sideways")
	if code != exitBadInput {
		t.Fatalf("exit = %d (stderr %s)", code, stderr)
	}
}

func shaFrom(t *testing.T, out string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^sha256:\s+([0-9a-f]{64})$`)
	m := re.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no sha256 in output:\n%s", out)
	}
	return m[1]
}

func deviationFrom(t *testing.T, out string) int64 {
	t.Helper()
	re := regexp.MustCompile(`max-cross-track-deviation: \S+ \((-?\d+) ns\)`)
	m := re.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no deviation in output:\n%s", out)
	}
	var v int64
	for _, c := range m[1] {
		if c == '-' {
			continue
		}
		v = v*10 + int64(c-'0')
	}
	return v
}
