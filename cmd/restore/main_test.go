package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunAllCasesAcceptance(t *testing.T) {
	dir := t.TempDir()
	results, sha, err := runAllCases(dir, 42, 120, 0.08)
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) != 64 {
		t.Fatalf("manifest sha must be 64 hex chars, got %d", len(sha))
	}
	wantCases := map[string]bool{
		"normal": true, "truncated_box": true, "corrupt_sample": true,
		"missing_index": true, "rtp_window_loss": true, "clock_overflow": true,
	}
	seen := map[string]caseResult{}
	for _, r := range results {
		seen[r.name] = r
		if !wantCases[r.name] {
			t.Fatalf("unexpected case %s", r.name)
		}
		// Every case prints the four required quantitative fields.
		if r.recoveredRate < 0 || r.recoveredRate > 1 {
			t.Fatalf("case %s invalid recovered rate %f", r.name, r.recoveredRate)
		}
		if r.syncErrorMs < 0 || r.p95LatencyMs < 0 {
			t.Fatalf("case %s negative latency metric", r.name)
		}
	}
	if len(seen) != len(wantCases) {
		t.Fatalf("missing cases: got %d want %d", len(seen), len(wantCases))
	}
	// Damaged paths must carry a concrete byte offset (window loss is purely
	// transport-side and has no container offset).
	if seen["truncated_box"].offset < 0 {
		t.Fatal("truncated case must report a byte offset")
	}
	if seen["corrupt_sample"].offset < 0 {
		t.Fatal("corrupt case must report a byte offset")
	}
	if seen["missing_index"].offset < 0 {
		t.Fatal("missing index case must report a rebuilt offset")
	}
	if ovf := seen["clock_overflow"]; ovf.offset < 0 || ovf.exitCode != 2 {
		t.Fatalf("clock overflow must preserve an offset and exit 2: %+v", ovf)
	}
	// Per-case manifests retain exit_code=2 for recovered damage, but none may
	// hit a hard threshold failure (exit 1); the self-test driver itself exits
	// 0 whenever every case stays below its thresholds.
	for name, r := range seen {
		if r.exitCode == 1 {
			t.Fatalf("case %s hit a hard threshold failure: %+v", name, r)
		}
	}
}

func TestRunAllCasesDeterministic(t *testing.T) {
	a, shaA, err := runAllCases(t.TempDir(), 42, 60, 0.08)
	if err != nil {
		t.Fatal(err)
	}
	b, shaB, err := runAllCases(t.TempDir(), 42, 60, 0.08)
	if err != nil {
		t.Fatal(err)
	}
	if shaA != shaB {
		t.Fatalf("manifest digest differs between runs: %s vs %s", shaA, shaB)
	}
	for i := range a {
		if a[i].manifestSHA != b[i].manifestSHA ||
			a[i].recoveredRate != b[i].recoveredRate ||
			a[i].syncErrorMs != b[i].syncErrorMs ||
			a[i].offset != b[i].offset {
			t.Fatalf("case %s not reproducible", a[i].name)
		}
	}
}

func TestEvidenceArtifactsWritten(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runAllCases(dir, 42, 24, 0.05); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "normal", "run-manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("run manifest missing: %v", err)
	}
	stagePath := filepath.Join(dir, "normal", "stage-validate.json")
	if _, err := os.Stat(stagePath); err != nil {
		t.Fatalf("stage evidence missing: %v", err)
	}
	frames, err := filepath.Glob(filepath.Join(dir, "normal", "frames", "*.ppm"))
	if err != nil || len(frames) != 24 {
		t.Fatalf("expected 24 PPM frames, got %d (err=%v)", len(frames), err)
	}
}

func TestFrameCapEnforced(t *testing.T) {
	dir := t.TempDir()
	// 10001 frames must be rejected before rendering allocates output.
	_, _, err := runAllCases(dir, 1, 10001, 0.0)
	if err == nil {
		t.Fatal("frame count above 10000 must be rejected")
	}
}
