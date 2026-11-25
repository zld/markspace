package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(args ...string) (code int, out, errOut string) {
	var stdout, stderr bytes.Buffer
	code = run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func extractKV(output, key string) string {
	prefix := key + "="
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

func TestCLIDemoSuccess(t *testing.T) {
	code, out, errOut := runCLI("demo", "--format", "hls,dash", "--late-ms", "500", "--queue-bytes", "1048576")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s\n%s", code, errOut, out)
	}
	if got := extractKV(out, "samples"); got != "8" {
		t.Fatalf("samples=%q\n%s", got, out)
	}
	if got := extractKV(out, "time_delta_ticks"); got != "0" {
		t.Fatalf("time_delta_ticks=%q", got)
	}
	if got := extractKV(out, "byte_delta"); got != "0" {
		t.Fatalf("byte_delta=%q", got)
	}
	if got := extractKV(out, "mapping_missing"); got != "0" {
		t.Fatalf("mapping_missing=%q", got)
	}
	if got := extractKV(out, "failure_location"); got != "none" {
		t.Fatalf("failure_location=%q", got)
	}
	if got := extractKV(out, "sha256"); len(got) != 64 {
		t.Fatalf("sha256=%q", got)
	}
	if strings.Count(out, "\nsample ") != 8 {
		t.Fatalf("expected 8 sample lines:\n%s", out)
	}
}

func TestCLIDemoRepeatableDigest(t *testing.T) {
	_, a, _ := runCLI("demo", "--format", "hls,dash", "--late-ms", "500", "--queue-bytes", "1048576")
	_, b, _ := runCLI("demo", "--format", "hls,dash", "--late-ms", "500", "--queue-bytes", "1048576")
	if extractKV(a, "sha256") != extractKV(b, "sha256") {
		t.Fatalf("digest differs between runs")
	}
}

func TestCLITruncatedFault(t *testing.T) {
	code, out, _ := runCLI("demo", "--fault", "truncated-box")
	if code != 2 {
		t.Fatalf("exit=%d want 2\n%s", code, out)
	}
	loc := extractKV(out, "failure_location")
	if !strings.Contains(loc, ".m4s@") {
		t.Fatalf("failure_location=%q", loc)
	}
	if !strings.Contains(extractKV(out, "error"), ".m4s@") {
		t.Fatalf("error lacks segment+offset: %s", out)
	}
}

func TestCLILateCheckpointFault(t *testing.T) {
	code, out, errOut := runCLI("demo", "--fault", "late-with-checkpoint", "--late-ms", "500")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s\n%s", code, errOut, out)
	}
	if extractKV(out, "late_recomputed") != "1" {
		t.Fatalf("late_recomputed=%q", extractKV(out, "late_recomputed"))
	}
	if extractKV(out, "late_rejected") != "1" {
		t.Fatalf("late_rejected=%q", extractKV(out, "late_rejected"))
	}
	if extractKV(out, "time_delta_ticks") != "0" || extractKV(out, "byte_delta") != "0" {
		t.Fatalf("deltas: %s", out)
	}
	if !strings.Contains(out, "resumed from") {
		t.Fatalf("missing recovery trace:\n%s", out)
	}
	// Repeated runs must agree.
	_, out2, _ := runCLI("demo", "--fault", "late-with-checkpoint", "--late-ms", "500")
	if extractKV(out, "sha256") != extractKV(out2, "sha256") {
		t.Fatalf("recovery digest differs across runs")
	}
}

func TestCLIUsageErrors(t *testing.T) {
	cases := [][]string{
		{"demo", "--format", "bogus"},
		{"demo", "--format", ""},
		{"demo", "--late-ms", "-1"},
		{"demo", "--queue-bytes", "0"},
		{"demo", "--fault", "nope"},
		{"demo", "extra"},
		{"nonsense"},
		{},
	}
	for _, args := range cases {
		code, _, _ := runCLI(args...)
		if code != 1 {
			t.Errorf("args %v: exit=%d want 1", args, code)
		}
	}
}

func TestCLIWorkDirKept(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "fixtures")
	code, out, errOut := runCLI("demo", "--format", "hls", "--workdir", sub)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(sub, "main", "video", "index.m3u8")); err != nil {
		t.Fatalf("fixtures not written to workdir: %v", err)
	}
	if !strings.Contains(out, "fixtures_root="+sub) {
		t.Fatalf("missing fixtures_root: %s", out)
	}
}

func TestCLIOutputContainsRequiredKeys(t *testing.T) {
	_, out, _ := runCLI("demo", "--format", "hls,dash", "--late-ms", "500", "--queue-bytes", "1048576")
	required := []string{
		"samples", "late_recomputed", "late_rejected", "time_delta_ticks",
		"byte_delta", "failure_location", "sha256",
	}
	for _, key := range required {
		if !strings.Contains(out, "\n"+key+"=") {
			t.Errorf("missing output key %q\n%s", key, out)
		}
	}
}
