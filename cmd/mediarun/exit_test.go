package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runStreams drives the CLI with explicit pipes, returning the exit code and
// everything written to either stream.
func runStreams(t *testing.T, args ...string) (int, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	code := run(args, outW, errW)
	outW.Close()
	errW.Close()
	var out, errOut bytes.Buffer
	out.ReadFrom(outR)
	errOut.ReadFrom(errR)
	return code, out.String() + errOut.String()
}

func TestExitCodeDamagedInputInProcess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	code, out := runStreams(t, "--out", dir, "--generate", "--inject", "checksum", "--verify")
	if code != exitDamaged {
		t.Fatalf("exit = %d want %d\n%s", code, exitDamaged, out)
	}
	if !strings.Contains(out, "exit_code=2") {
		t.Fatalf("missing exit_code=2 line:\n%s", out)
	}
	if !strings.Contains(out, "checksum_mismatch") || !strings.Contains(out, "offset=") {
		t.Fatalf("missing fault detail:\n%s", out)
	}
}

func TestExitCodeHealthyInProcess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	code, out := runStreams(t, "--out", dir, "--generate", "--verify")
	if code != exitOK {
		t.Fatalf("exit = %d want %d\n%s", code, exitOK, out)
	}
	if !strings.Contains(out, "exit_code=0") {
		t.Fatalf("missing exit_code=0 line:\n%s", out)
	}
}

func TestExitCodeInternalErrorInProcess(t *testing.T) {
	// A run directory with no generated inputs is an internal error.
	dir := filepath.Join(t.TempDir(), "run")
	code, out := runStreams(t, "--out", dir, "--verify")
	if code != exitInternal {
		t.Fatalf("exit = %d want %d\n%s", code, exitInternal, out)
	}
	if !strings.Contains(out, "run_error") {
		t.Fatalf("missing run_error:\n%s", out)
	}
}
