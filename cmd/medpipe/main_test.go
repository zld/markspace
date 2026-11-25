package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunSelfTestCommand(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--selftest", "--seed", "17", "--budget-ms", "5000",
		"--max-bytes", "1048576", "--out-dir", dir,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	line := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(line, "roundtrip=100% keyframe_hit=100% duplicates=1 corrupt=1 truncated=1 recovered=1 segment_sha256=") {
		t.Fatalf("unexpected summary: %q", line)
	}
	if len(line)-strings.Index(line, "segment_sha256=")-len("segment_sha256=") != 64 {
		t.Fatalf("hash not 64 hex chars: %q", line)
	}
}

func TestRunParameterErrorExitTwo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--unknown"}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown flag code=%d", code)
	}
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("missing command code=%d", code)
	}
}

func TestRunBudgetFailureExitOne(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--selftest", "--budget-ms", "0", "--out-dir", dir,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%s", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("budget failure emitted success output: %q", stdout.String())
	}
}
