package main

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCLIExitCodesAndResumeOutput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "accept")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	base := []string{
		"demo", "--workdir", dir, "--seed", "17", "--frames", "24",
		"--audio-packets", "64", "--chunk", "8",
		"--inject", "bad-sha,truncated,late,duplicate",
	}
	if code := run(base, stdout, stderr); code != 0 {
		t.Fatalf("demo code=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"frame_total=24", "bad_sha=1", "truncated=1", "late_packets=1", "duplicate_packets=1", "定位误差=0", "chunk_count=3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("demo output missing %q:\n%s", want, out)
		}
	}

	strictOut := &bytes.Buffer{}
	strictErr := &bytes.Buffer{}
	strictArgs := append(append([]string{}, base...), "--strict-corrupt")
	if code := run(strictArgs, strictOut, strictErr); code != 2 {
		t.Fatalf("strict demo code=%d want 2", code)
	}

	syncOut := &bytes.Buffer{}
	syncCode := run([]string{"resume", "--workdir", dir, "--av-offset-ms", "50"}, syncOut, &bytes.Buffer{})
	if syncCode != 3 {
		t.Fatalf("sync code=%d want 3 output=%s", syncCode, syncOut.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"resume", "--workdir", dir, "--interrupt-after", "1"}, stdout, stderr); code != 10 {
		t.Fatalf("interrupt code=%d want 10 stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "exit_code=10") {
		t.Fatalf("interrupt summary missing exit_code=10:\n%s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"resume", "--workdir", dir}, stdout, stderr); code != 0 {
		t.Fatalf("resume code=%d stderr=%s", code, stderr.String())
	}
	out = stdout.String()
	if !strings.Contains(out, "resumed_from_chunk=1") {
		t.Fatalf("missing resume marker:\n%s", out)
	}
	if !regexp.MustCompile(`(?m)^sha256=[0-9a-f]{64}$`).MatchString(out) {
		t.Fatalf("missing 64-char sha256:\n%s", out)
	}

	validateOut := &bytes.Buffer{}
	if code := run([]string{"validate", "--workdir", dir}, validateOut, &bytes.Buffer{}); code != 2 {
		t.Fatalf("validate code=%d want 2", code)
	}

	channelOut := &bytes.Buffer{}
	channelCode := run([]string{"resume", "--workdir", dir, "--source-layout", "unknown-9.1"}, channelOut, &bytes.Buffer{})
	if channelCode != 3 {
		t.Fatalf("channel code=%d want 3", channelCode)
	}
}
