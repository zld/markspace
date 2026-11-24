package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCLIDemoAcceptanceCommand(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cli")
	code, out, errOut := runCLI(t,
		"demo", "--seed", "17", "--loss-burst", "3", "--reorder", "4",
		"--duplicate", "2", "--bandwidth", "4000,900,2200", "--workdir", dir,
	)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s\nstdout tail:\n%s", code, errOut, tail(out))
	}
	for _, want := range []string{
		"roundtrip_rate=1.000",
		"keyframe_hit_rate=1.000",
		"segment_sha256=",
		"duplicate_count=2",
		"missing_count=3",
		"index_rebuilt=true",
		"hashes_match=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
	if strings.Contains(out, "check    ") && strings.Contains(out, "pass=false") {
		t.Errorf("output contains a failing check:\n%s", out)
	}
}

func TestCLIDemoRepeatTwoHashesEqual(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repeat")
	code, out, errOut := runCLI(t, "demo", "--seed", "17", "--repeat", "2", "--workdir", dir)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s\n%s", code, errOut, tail(out))
	}
	if !strings.Contains(out, "repeat  runs=2 identical=true") {
		t.Fatalf("repeat verdict missing/incorrect:\n%s", out)
	}
	if !strings.Contains(out, "repeat=2 hashes_match=true") {
		t.Fatalf("summary repeat match missing:\n%s", out)
	}
}

func TestCLIRejectsBadBandwidth(t *testing.T) {
	if _, err := parseBandwidth("100,0"); err == nil {
		t.Fatal("expected error for zero bandwidth step")
	}
	if _, err := parseBandwidth("abc"); err == nil {
		t.Fatal("expected parse error")
	}
	got, err := parseBandwidth("4000, 900")
	if err != nil || len(got) != 2 || got[1] != 900 {
		t.Fatalf("parseBandwidth = %v,%v", got, err)
	}
}

func TestCLIUsageAndBadCommand(t *testing.T) {
	code, _, _ := runCLI(t, "frobnicate")
	if code != 2 {
		t.Fatalf("unknown command exit = %d, want 2", code)
	}
	code, _, _ = runCLI(t)
	if code != 2 {
		t.Fatalf("no args exit = %d, want 2", code)
	}
}

func tail(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) > 25 {
		lines = lines[len(lines)-25:]
	}
	return strings.Join(lines, "\n")
}
