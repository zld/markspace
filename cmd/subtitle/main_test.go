package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"subtitle/internal/fixture"
)

func newWriters() (*bytes.Buffer, *bytes.Buffer) {
	return &bytes.Buffer{}, &bytes.Buffer{}
}

func prepareDemoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := fixture.Write(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestHelpExitZero(t *testing.T) {
	out, errOut := newWriters()
	if code := run(nil, out, errOut); code != 0 || !strings.Contains(out.String(), "verify") {
		t.Fatalf("help code=%d out=%q", code, out.String())
	}
}

func TestUnknownSubcommandExitTwo(t *testing.T) {
	_, errOut := newWriters()
	if code := run([]string{"frobnicate"}, &bytes.Buffer{}, errOut); code != 2 {
		t.Fatalf("code=%d", code)
	}
}

func TestFixtureAndVerifyHappyPath(t *testing.T) {
	dir := prepareDemoDir(t)
	out, errOut := newWriters()
	code := run([]string{"verify",
		"--input", filepath.Join(dir, "scene.srt"),
		"--output", filepath.Join(dir, "scene.ass"),
		"--to", "ass", "--roundtrip", "srt",
		"--fps", "24000/1001", "--overlap", "drop-later",
	}, out, errOut)
	if code != 0 {
		t.Fatalf("verify code=%d stderr=%s stdout=%s", code, errOut.String(), out.String())
	}
	report := out.String()
	for _, want := range []string{
		"cue=3 start_frame=120 end_frame=144 roundtrip_delta_ms=5 dropped=false",
		"input=4 output=3 dropped=1 max_abs_delta_ms=5 encoding=utf-8",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestBadArgumentsExitTwo(t *testing.T) {
	dir := prepareDemoDir(t)
	_, errOut := newWriters()
	code := run([]string{"verify", "--input", filepath.Join(dir, "scene.srt"),
		"--output", filepath.Join(dir, "x.ass"), "--to", "ass", "--fps", "nope"},
		&bytes.Buffer{}, errOut)
	if code != 2 {
		t.Fatalf("bad fps code=%d", code)
	}
}

func TestMissingFileExitTwo(t *testing.T) {
	_, errOut := newWriters()
	code := run([]string{"verify", "--input", filepath.Join(t.TempDir(), "nope.srt"),
		"--output", filepath.Join(t.TempDir(), "o.ass"), "--to", "ass"},
		&bytes.Buffer{}, errOut)
	if code != 2 {
		t.Fatalf("missing file code=%d", code)
	}
}

func TestOverwriteRefusedExitTwo(t *testing.T) {
	dir := prepareDemoDir(t)
	outPath := filepath.Join(dir, "scene.ass")
	if err := os.WriteFile(outPath, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut := newWriters()
	code := run([]string{"verify", "--input", filepath.Join(dir, "scene.srt"),
		"--output", outPath, "--to", "ass"}, out, errOut)
	if code != 2 || !strings.Contains(out.String(), "already exists") {
		t.Fatalf("code=%d report=%s err=%s", code, out.String(), errOut.String())
	}
}

func TestVerificationFailureExitOne(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "zero.srt")
	// Zero-duration cue passes parsing but must be rejected by frame mapping.
	content := "1\n00:00:01,000 --> 00:00:01,000\nzero length\n"
	if err := os.WriteFile(bad, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := newWriters()
	code := run([]string{"verify", "--input", bad,
		"--output", filepath.Join(dir, "zero.ass"), "--to", "ass"}, out, errDrop())
	if code != 1 {
		t.Fatalf("verification failure code=%d report=%s", code, out.String())
	}
}

func TestBatchPartialFailureExitThree(t *testing.T) {
	dir := prepareDemoDir(t)
	bad := filepath.Join(dir, "broken.srt")
	if err := os.WriteFile(bad, []byte("1\nbroken timing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := newWriters()
	code := run([]string{"verify", "--input", dir, "--to", "ass",
		"--jobs", "2", "--force"}, out, errDrop())
	if code != 3 {
		t.Fatalf("partial batch code=%d report=%s", code, out.String())
	}
	if !strings.Contains(out.String(), "batch: files=3 failed=1") {
		t.Fatalf("missing batch summary:\n%s", out.String())
	}
}

func errDrop() *bytes.Buffer { return &bytes.Buffer{} }
