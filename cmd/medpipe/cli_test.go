package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

type cliResult struct {
	code   int
	stdout string
	stderr string
}

func runCLI(t *testing.T, args ...string) cliResult {
	t.Helper()
	var out, errBuf bytes.Buffer
	code := Main(args, &out, &errBuf)
	return cliResult{code: code, stdout: out.String(), stderr: errBuf.String()}
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fixtures")
	res := runCLI(t, "fixture", "--dir", dir, "--seed", "7")
	if res.code != 0 {
		t.Fatalf("fixture code=%d stdout=%s stderr=%s", res.code, res.stdout, res.stderr)
	}
	return dir
}

func decodeSummary(t *testing.T, out string) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("decode summary: %v\noutput=%s", err, out)
	}
	return d
}

func TestCLINormalIdempotentAndRecovery(t *testing.T) {
	fdir := fixtureDir(t)
	manifest := filepath.Join(fdir, "normal.json")
	root := filepath.Join(t.TempDir(), "run")

	first := runCLI(t, "run", "--manifest", manifest, "--root", root, "--job-id", "demo", "--budget-units", "80", "--json")
	if first.code != 0 {
		t.Fatalf("first code=%d stderr=%s", first.code, first.stderr)
	}
	d := decodeSummary(t, first.stdout)
	for key, want := range map[string]any{
		"tracks": float64(3), "samples": float64(18), "wrap_events": float64(1),
		"empty_edits": float64(1), "repairs": float64(1), "estimate": float64(72),
		"actual": float64(75), "variance_pct": 4.17, "max_residual_ticks": float64(0),
		"verdict": "PASS", "emitted": float64(4),
	} {
		if d[key] != want {
			t.Fatalf("field %s=%v want %v", key, d[key], want)
		}
	}
	line, _ := d["summary_line"].(string)
	for _, token := range []string{"tracks=3", "estimate=72", "actual=75", "variance_pct=4.17", "max_residual_ticks=0", "verdict=PASS"} {
		if !strings.Contains(line, token) {
			t.Fatalf("summary line missing %q: %s", token, line)
		}
	}
	if !strings.Contains(first.stderr, "anomaly_start_tick=7000") ||
		!strings.Contains(first.stderr, "original_offset_ticks=-2000") ||
		!strings.Contains(first.stderr, "repair_offset_ticks=2000") {
		t.Fatalf("human terminal report missing repair details:\n%s", first.stderr)
	}

	repeat := runCLI(t, "run", "--manifest", manifest, "--root", root, "--job-id", "demo", "--budget-units", "80", "--json")
	if repeat.code != 0 {
		t.Fatalf("repeat code=%d stderr=%s", repeat.code, repeat.stderr)
	}
	r := decodeSummary(t, repeat.stdout)
	if r["manifest_sha256"] != d["manifest_sha256"] || r["artifact_sha256"] != d["artifact_sha256"] {
		t.Fatal("repeat hashes changed")
	}
	if r["emitted"] != float64(0) {
		t.Fatalf("repeat emitted=%v", r["emitted"])
	}

	recoveryRoot := filepath.Join(t.TempDir(), "recovery")
	fault := runCLI(t, "run", "--manifest", manifest, "--root", recoveryRoot, "--job-id", "cut", "--budget-units", "80", "--fault", "after-artifact")
	if fault.code != 4 {
		t.Fatalf("fault code=%d stderr=%s", fault.code, fault.stderr)
	}
	resume := runCLI(t, "resume", "--root", recoveryRoot, "--job-id", "cut", "--budget-units", "80", "--json")
	if resume.code != 0 {
		t.Fatalf("resume code=%d stderr=%s stdout=%s", resume.code, resume.stderr, resume.stdout)
	}
	rd := decodeSummary(t, resume.stdout)
	if rd["artifact_sha256"] != d["artifact_sha256"] || rd["manifest_sha256"] != d["manifest_sha256"] {
		t.Fatalf("recovery hash mismatch: %v vs %v", rd["artifact_sha256"], d["artifact_sha256"])
	}
}

func TestCLIBadRangeAndBadJumpExitCodes(t *testing.T) {
	fdir := fixtureDir(t)
	rangeRes := runCLI(t, "run",
		"--manifest", filepath.Join(fdir, "bad-range.json"),
		"--root", filepath.Join(t.TempDir(), "range"), "--job-id", "bad", "--json")
	if rangeRes.code != 2 {
		t.Fatalf("range code=%d output=%s%s", rangeRes.code, rangeRes.stdout, rangeRes.stderr)
	}
	combined := rangeRes.stdout + rangeRes.stderr
	for _, token := range []string{"E_SAMPLE_RANGE", "track=range-bad", "sample=2", "byte_offset=412"} {
		if !strings.Contains(combined, token) {
			t.Fatalf("range output missing %q:\n%s", token, combined)
		}
	}

	jumpRes := runCLI(t, "run",
		"--manifest", filepath.Join(fdir, "bad-jump.json"),
		"--root", filepath.Join(t.TempDir(), "jump"), "--job-id", "jump", "--json")
	if jumpRes.code != 3 {
		t.Fatalf("jump code=%d output=%s%s", jumpRes.code, jumpRes.stdout, jumpRes.stderr)
	}
	combined = jumpRes.stdout + jumpRes.stderr
	for _, token := range []string{
		"E_TRACK_ISOLATED", "verdict\": \"ISOLATED",
		"anomaly_start_tick=540000", "anomaly_end_tick=549000",
		"align-bad-jump", "package-bad-jump",
	} {
		if !strings.Contains(combined, token) {
			t.Fatalf("jump output missing %q:\n%s", token, combined)
		}
	}
}
