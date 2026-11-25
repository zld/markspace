package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func invoke(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestNormalExitZero(t *testing.T) {
	dir := t.TempDir()
	code, stdout, stderr := invoke(t, "-fixture", "normal", "-seed", "17", "-out", dir)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, stderr)
	}
	for _, want := range []string{"frames=", "segments=", "pixels mean=", "loudness peak=", "alignment", "sha256 state=", "sha256 manifest="} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
}

func TestTruncatedExitTwo(t *testing.T) {
	dir := t.TempDir()
	code, stdout, _ := invoke(t, "-fixture", "truncated", "-seed", "17", "-out", dir)
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(stdout, "track=") || !strings.Contains(stdout, "offset=") {
		t.Fatalf("truncation location not reported:\n%s", stdout)
	}
}

func TestDamagedExitTwoWithGaps(t *testing.T) {
	dir := t.TempDir()
	code, stdout, _ := invoke(t, "-fixture", "damaged", "-seed", "17", "-out", dir)
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(stdout, "gaps=") {
		t.Fatal("gap count not printed")
	}
}

func TestLateExitZero(t *testing.T) {
	dir := t.TempDir()
	code, _, stderr := invoke(t, "-fixture", "late", "-seed", "17", "-out", dir)
	if code != 0 {
		t.Fatalf("exit %d, want 0, stderr=%s", code, stderr)
	}
}

func TestMissingOutIsUsageError(t *testing.T) {
	code, _, stderr := invoke(t, "-fixture", "normal")
	if code != 64 {
		t.Fatalf("exit %d, want 64", code)
	}
	if !strings.Contains(stderr, "-out") {
		t.Fatalf("usage error not reported: %s", stderr)
	}
}

func TestUnknownFixtureIsUsageError(t *testing.T) {
	dir := t.TempDir()
	code, _, _ := invoke(t, "-fixture", "nope", "-out", dir)
	if code != 64 {
		t.Fatalf("exit %d, want 64", code)
	}
}

func TestInvalidLateWindowIsUsageError(t *testing.T) {
	dir := t.TempDir()
	code, _, _ := invoke(t, "-fixture", "normal", "-out", dir, "-late-window", "5")
	if code != 64 {
		t.Fatalf("exit %d, want 64", code)
	}
}

func TestParseDurationNS(t *testing.T) {
	cases := map[string]int64{
		"2s":     2_000_000_000,
		"500ms":  500_000_000,
		"1500us": 1_500_000,
		"10ns":   10,
	}
	for in, want := range cases {
		got, err := parseDurationNS(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got != want {
			t.Fatalf("%s: got %d want %d", in, got, want)
		}
	}
	for _, bad := range []string{"", "5", "abc", "-1s"} {
		if _, err := parseDurationNS(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestTwoRunsIdenticalManifest(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	if code, _, stderr := invoke(t, "-fixture", "normal", "-seed", "17", "-out", dirA); code != 0 {
		t.Fatalf("run A exit %d: %s", code, stderr)
	}
	if code, _, stderr := invoke(t, "-fixture", "normal", "-seed", "17", "-out", dirB); code != 0 {
		t.Fatalf("run B exit %d: %s", code, stderr)
	}
	a, err := os.ReadFile(filepath.Join(dirA, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dirB, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("manifests differ between identical runs")
	}
}

func TestOutputPathTraversalRejected(t *testing.T) {
	code, _, stderr := invoke(t, "-fixture", "normal", "-out", "/tmp/../etc")
	if code == 0 {
		t.Fatal("path traversal accepted")
	}
	if !strings.Contains(stderr, "path") && !strings.Contains(stderr, "fatal") {
		t.Fatalf("traversal not reported: %s", stderr)
	}
}
