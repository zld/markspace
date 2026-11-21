package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureOutput redirects stdout/stderr while fn runs, then restores them.
func captureOutput(fn func()) (string, string) {
	origOut, origErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr
	done := make(chan struct{})
	var outBuf, errBuf bytes.Buffer
	go func() { _, _ = outBuf.ReadFrom(rOut); close(done) }()
	doneErr := make(chan struct{})
	go func() { _, _ = errBuf.ReadFrom(rErr); close(doneErr) }()
	fn()
	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout, os.Stderr = origOut, origErr
	<-done
	<-doneErr
	return outBuf.String(), errBuf.String()
}

func TestRunDemoHealthyExitZero(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	code := -1
	out, _ := captureOutput(func() {
		code = run([]string{"demo", "--out", dir,
			"--duration", "12s", "--segment", "3s",
			"--tiers", "450k,900k"})
	})
	if code != exitOK {
		t.Fatalf("exit code: %d", code)
	}
	if !strings.Contains(out, "result: PASS") {
		t.Fatalf("missing PASS:\n%s", out)
	}
	if !strings.Contains(out, "validation_failures=0") {
		t.Fatalf("missing zero failures:\n%s", out)
	}
}

func TestRunDemoFaultsExitTwo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	code := -1
	out, _ := captureOutput(func() {
		code = run([]string{"demo", "--out", dir,
			"--duration", "36s", "--segment", "3s",
			"--tiers", "450k,900k,1800k",
			"--drop-segment", "900k:4",
			"--swap-segments", "1800k:2:3"})
	})
	if code != exitVerify {
		t.Fatalf("exit code: %d, want 2", code)
	}
	for _, want := range []string{
		"bitrate_bps", "validation_failures=2", "result: FAIL",
		"missing_segment", "out_of_order",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunDemoJSONOutput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	out, _ := captureOutput(func() {
		code := run([]string{"demo", "--out", dir, "--json",
			"--duration", "6s", "--segment", "3s", "--tiers", "450k"})
		if code != exitOK {
			t.Fatalf("json healthy run exit: %d", code)
		}
	})
	if !strings.Contains(out, `"validation_failures": 0`) {
		t.Fatalf("json missing field:\n%s", out)
	}
}

func TestRunDemoBadFlagsExitOne(t *testing.T) {
	cases := [][]string{
		{"demo"}, // missing --out
		{"demo", "--out", t.TempDir(), "--duration", "nope"},
		{"demo", "--out", t.TempDir(), "--tiers", ""},
		{"demo", "--out", t.TempDir(), "--drop-segment", "bad"},
	}
	for _, c := range cases {
		if code := run(c); code != exitUsageIO {
			t.Fatalf("args %v exit %d, want 1", c, code)
		}
	}
}

func TestRunUnknownSubcommand(t *testing.T) {
	if code := run([]string{"frobnicate"}); code != exitUsageIO {
		t.Fatalf("unknown subcommand exit: %d", code)
	}
}
