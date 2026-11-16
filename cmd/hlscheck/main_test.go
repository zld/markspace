package main

import (
	"path/filepath"
	"testing"
)

// The demo must exit 0 when it accurately reports the injected faults.
func TestDemoWithFaults(t *testing.T) {
	dir := t.TempDir()
	code, err := cmdDemo([]string{
		"--workdir", dir,
		"--profiles", "450,900,1800",
		"--segments", "6",
		"--drop", "900:2",
		"--swap", "1800:1,2",
		"--tamper", "450:3",
	})
	if err != nil {
		t.Fatalf("cmdDemo error: %v", err)
	}
	if code != exitOK {
		t.Errorf("exit = %d, want 0", code)
	}
	// The faulted output must fail a plain check with exit 1.
	code, err = cmdCheck([]string{"--root", filepath.Join(dir, "hls")})
	if err != nil {
		t.Fatalf("cmdCheck error: %v", err)
	}
	if code != exitInconsistent {
		t.Errorf("check exit = %d, want 1", code)
	}
}

// A fault-free demo run is clean end to end.
func TestDemoClean(t *testing.T) {
	dir := t.TempDir()
	code, err := cmdDemo([]string{"--workdir", dir, "--profiles", "450,900", "--segments", "4"})
	if err != nil {
		t.Fatalf("cmdDemo error: %v", err)
	}
	if code != exitOK {
		t.Errorf("exit = %d, want 0", code)
	}
	code, err = cmdCheck([]string{"--root", filepath.Join(dir, "hls")})
	if err != nil {
		t.Fatalf("cmdCheck error: %v", err)
	}
	if code != exitOK {
		t.Errorf("check exit = %d, want 0", code)
	}
}

// Argument and I/O failures map to exit code 2.
func TestUsageErrors(t *testing.T) {
	if _, err := cmdDemo([]string{"--profiles", "abc"}); err == nil {
		t.Error("bad profiles: want error")
	}
	if _, err := cmdCheck([]string{"--root", "/nonexistent-dir-xyz"}); err == nil {
		t.Error("missing root: want error")
	}
	if _, err := cmdCut([]string{"--out", t.TempDir()}); err == nil {
		t.Error("cut without input: want error")
	}
}
