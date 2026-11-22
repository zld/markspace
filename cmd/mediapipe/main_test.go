package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	errOut, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatal(err)
	}
	code := run(args, out, errOut)
	out.Close()
	errOut.Close()
	o, _ := os.ReadFile(out.Name())
	e, _ := os.ReadFile(errOut.Name())
	return string(o), string(e), code
}

func TestCLIDemoAcceptance(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, code := runCLI(t, "demo", "--seed", "17", "--jitter-ms", "80",
		"--loss-burst", "3", "--checkpoint-dir", filepath.Join(dir, "cp"))
	if code != exitOK {
		t.Fatalf("exit %d stderr=%s", code, stderr)
	}
	for _, field := range []string{
		"roundtrip_match_rate=1.000",
		"keyframe_index_hit_rate=1.000",
		"reordered_packets=",
		"recovered_packets=",
		"unrecovered_gaps=",
		"cache_hits=",
		"cache_rejected=",
		"checkpoint_resumes=",
		"segment_hashes[1]=",
		"segment_hashes[6]=",
	} {
		if !strings.Contains(stdout, field) {
			t.Fatalf("missing %q in output:\n%s", field, stdout)
		}
	}
	hashes := map[string]bool{}
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "segment_hashes[") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 || len(parts[1]) != 64 {
				t.Fatalf("bad hash line %q", line)
			}
			if hashes[parts[1]] {
				t.Fatalf("duplicate segment hash %s", parts[1])
			}
			hashes[parts[1]] = true
		}
	}
	if len(hashes) != 6 {
		t.Fatalf("expected 6 distinct segment hashes, got %d", len(hashes))
	}
}

func TestCLIUsageErrorsExitTwo(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"bogus"},
		{"demo", "--seed", "-1"},
		{"demo", "--window", "0"},
		{"demo", "--jitter-ms", "9999"},
		{"demo", "extra-arg"},
	} {
		if _, _, code := runCLI(t, args...); code != exitUsage {
			t.Fatalf("args %v exited %d, want %d", args, code, exitUsage)
		}
	}
}

func TestCLIJSONSummary(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, code := runCLI(t, "demo", "--seed", "5", "--json",
		"--checkpoint-dir", filepath.Join(dir, "cp"))
	if code != exitOK {
		t.Fatalf("exit %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "\"roundtrip_match_rate\":1") {
		t.Fatalf("json summary missing rate: %s", stdout)
	}
}

func TestCLIInterruptResumeDeterminism(t *testing.T) {
	dir := t.TempDir()
	cp := filepath.Join(dir, "cp")
	if _, stderr, code := runCLI(t, "demo", "--checkpoint-dir", cp, "--interrupt-after", "2"); code != exitOK {
		t.Fatalf("interrupt exit %d %s", code, stderr)
	}
	stdout, stderr, code := runCLI(t, "demo", "--checkpoint-dir", cp, "--resume")
	if code != exitOK {
		t.Fatalf("resume exit %d %s", code, stderr)
	}
	if !strings.Contains(stdout, "checkpoint_resumes=1") {
		t.Fatalf("resume not reported: %s", stdout)
	}
	if !strings.Contains(stdout, "roundtrip_match_rate=1.000") {
		t.Fatalf("resumed run below threshold: %s", stdout)
	}
}
