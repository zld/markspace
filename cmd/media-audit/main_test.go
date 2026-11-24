package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI executes the adapter with args and captures stdout.
func runCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	errw, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatal(err)
	}
	defer errw.Close()
	code := run(args, f, errw)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), code
}

func TestGenerateInlineDirectoryForm(t *testing.T) {
	root := t.TempDir()
	out, code := runCLI(t, "-generate", filepath.Join(root, "samples"), "-out", filepath.Join(root, "run"), "-chunk", "32", "-resume")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	for _, want := range []string{
		"roundtrip_consistency=1.000",
		"keyframe_index_hit_rate=1.000",
		"segment_hash_match=1.000",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "samples", "normal", "fixtures.json")); err != nil {
		t.Fatalf("fixture manifest missing: %v", err)
	}
}

func TestRejectsBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{"-out", ""},
		{"-out", t.TempDir(), "-chunk", "0"},
		{"-out", t.TempDir(), "-interval", "nope"},
		{"-out", t.TempDir(), "-width", "0"},
	} {
		if _, code := runCLI(t, args...); code != exitFatal {
			t.Fatalf("%v: exit=%d want 1", args, code)
		}
	}
}

func TestMissingFixturesIsFatal(t *testing.T) {
	root := t.TempDir()
	_, code := runCLI(t, "-out", root, "-samples", filepath.Join(root, "nope"))
	if code != exitFatal {
		t.Fatalf("exit=%d", code)
	}
}

func TestScenarioEnvRouting(t *testing.T) {
	root := t.TempDir()
	samples := filepath.Join(root, "samples")
	if _, code := runCLI(t, "-generate", samples, "-out", filepath.Join(root, "gen")); code != exitOK {
		t.Fatal("generation failed")
	}
	t.Setenv("MEDIA_AUDIT_SCENARIO", "broken")
	out, code := runCLI(t, "-out", filepath.Join(root, "broken"), "-samples", samples, "-resume")
	if code != exitRecoverable {
		t.Fatalf("exit=%d want 2, out=%s", code, out)
	}
	if !strings.Contains(out, "code=index_fallback") {
		t.Fatalf("fallback not reported:\n%s", out)
	}
}

func TestUnsupportedTimecodeModeIsFatal(t *testing.T) {
	root := t.TempDir()
	samples := filepath.Join(root, "samples")
	if _, code := runCLI(t, "-generate", samples, "-out", filepath.Join(root, "gen")); code != exitOK {
		t.Fatal("generation failed")
	}
	if _, code := runCLI(t, "-out", filepath.Join(root, "run"), "-samples", samples, "-timecode-mode", "bogus"); code != exitFatal {
		t.Fatalf("exit=%d want 1", code)
	}
}
