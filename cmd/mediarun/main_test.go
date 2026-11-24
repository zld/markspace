package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI invokes the command with args and captures both streams.
func runCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	full := append([]string{"--out", filepath.Join(dir, "run")}, args...)
	code := run(full, outW, errW)
	outW.Close()
	errW.Close()
	var buf bytes.Buffer
	buf.ReadFrom(outR)
	var errBuf bytes.Buffer
	errBuf.ReadFrom(errR)
	return code, buf.String() + errBuf.String()
}

func TestCLIHealthyRun(t *testing.T) {
	code, out := runCLI(t, "--generate", "--resume", "--verify")
	if code != exitOK {
		t.Fatalf("exit = %d, out:\n%s", code, out)
	}
	for _, want := range []string{
		"roundtrip_match=100%",
		"keyframe_hit=100%",
		"segment_sha256_count=4",
		"segment_sha256_equal=1",
		"idempotent=1",
		"parse_errors=0",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestCLIDamagedRun(t *testing.T) {
	code, out := runCLI(t, "--generate", "--inject", "truncated,checksum", "--verify")
	if code != exitDamaged {
		t.Fatalf("exit = %d want %d, out:\n%s", code, exitDamaged, out)
	}
	if !strings.Contains(out, "parse_errors=2") && !strings.Contains(out, "parse_errors=3") {
		t.Fatalf("unexpected parse error count:\n%s", out)
	}
	if !strings.Contains(out, "first_error_offset=") {
		t.Fatalf("missing offset:\n%s", out)
	}
	if !strings.Contains(out, "checksum_mismatch") || !strings.Contains(out, "truncated_record") {
		t.Fatalf("missing fault categories:\n%s", out)
	}
	if !strings.Contains(out, "recovered_video=") {
		t.Fatalf("missing recovery count:\n%s", out)
	}
}

func TestCLIRejectsUnknownInjection(t *testing.T) {
	code, out := runCLI(t, "--generate", "--inject", "bogus")
	if code != exitInternal {
		t.Fatalf("exit = %d out:\n%s", code, out)
	}
	if !strings.Contains(out, "unknown injection") {
		t.Fatalf("missing error text:\n%s", out)
	}
}

func TestCLIMissingInputsIsInternalError(t *testing.T) {
	dir := t.TempDir()
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	code := run([]string{"--out", dir}, outW, errW)
	outW.Close()
	errW.Close()
	var buf bytes.Buffer
	buf.ReadFrom(errR)
	outR.Close()
	if code != exitInternal {
		t.Fatalf("exit = %d out:\n%s", code, buf.String())
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" a, b ,,c ")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v", got)
		}
	}
	if len(splitList("  ")) != 0 {
		t.Fatal("empty list not empty")
	}
}
