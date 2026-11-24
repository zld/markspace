package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// captureStdout runs fn and returns everything written to os.Stdout.
func captureStdout(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := fn()
	w.Close()
	os.Stdout = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), code
}

func TestDemoCleanOutputsAcceptedJSON(t *testing.T) {
	dir := t.TempDir()
	out, code := captureStdout(t, func() int {
		return run([]string{"demo", "--seed", "42", "--target-bitrate", "8000000", "--workdir", dir})
	})
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	for _, field := range []string{
		"target_bps", "actual_bps", "bitrate_delta_pct", "samples", "keyframes",
		"timestamp_repairs", "index_coverage_pct", "quality_psnr_db",
		"alignment_error_ms", "input_sha256", "output_sha256",
	} {
		if _, ok := got[field]; !ok {
			t.Fatalf("missing field %q in %v", field, out)
		}
	}
	if got["resumed"].(bool) {
		t.Fatal("cold run reported resumed")
	}
	if got["index_coverage_pct"].(float64) != 100 {
		t.Fatalf("coverage=%v", got["index_coverage_pct"])
	}
	if delta := got["bitrate_delta_pct"].(float64); delta > 5 || delta < -5 {
		t.Fatalf("bitrate delta %.3f%% outside 5%%", delta)
	}
	if align := got["alignment_error_ms"].(float64); align > 1 {
		t.Fatalf("alignment %.4fms outside 1ms", align)
	}
}

func TestDemoResumeReportsResumedAndSameDigest(t *testing.T) {
	dir := t.TempDir()
	_, code := captureStdout(t, func() int {
		return run([]string{"demo", "--seed", "42", "--target-bitrate", "8000000", "--workdir", dir})
	})
	if code != 0 {
		t.Fatalf("first exit=%d", code)
	}
	out, code := captureStdout(t, func() int {
		return run([]string{"demo", "--seed", "42", "--target-bitrate", "8000000", "--resume", "--workdir", dir})
	})
	if code != 0 {
		t.Fatalf("resume exit=%d", code)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if !got["resumed"].(bool) {
		t.Fatal("resume run did not report resumed=true")
	}
	first := firstDigest(t, filepath.Join(dir, "out", "report.json"))
	_ = first
	// The digest must match what a cold recompute would produce.
	outCold, _ := captureStdout(t, func() int {
		return run([]string{"demo", "--seed", "42", "--target-bitrate", "8000000", "--workdir", filepath.Join(dir, "..", "cold-"+filepath.Base(dir))})
	})
	var cold map[string]any
	if err := json.Unmarshal([]byte(outCold), &cold); err != nil {
		t.Fatal(err)
	}
	if cold["output_sha256"].(string) != got["output_sha256"].(string) {
		t.Fatalf("resumed digest %v != cold %v", got["output_sha256"], cold["output_sha256"])
	}
}

func firstDigest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r.Digest
}

func TestDemoCorruptExitsTwoWithStructuredFields(t *testing.T) {
	// Errors go to stderr; only the exit code and fields are asserted here.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldErr := os.Stderr
	os.Stderr = w
	code := run([]string{"demo", "--seed", "42", "--corrupt", "truncated-table", "--workdir", t.TempDir()})
	w.Close()
	os.Stderr = oldErr
	data, _ := io.ReadAll(r)
	if code != 2 {
		t.Fatalf("exit=%d want 2", code)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("stderr not JSON: %v\n%s", err, data)
	}
	for _, f := range []string{"stage", "track_id", "raw_offset", "error_code", "exit_code"} {
		if _, ok := payload[f]; !ok {
			t.Fatalf("missing %q in error: %s", f, data)
		}
	}
	if payload["exit_code"].(float64) != 2 {
		t.Fatalf("exit_code=%v want 2", payload["exit_code"])
	}
	if payload["raw_offset"].(float64) <= 0 {
		t.Fatalf("raw_offset not positive: %s", data)
	}
}

func TestDemoEditOverrunExitsTwo(t *testing.T) {
	_, w, _ := os.Pipe()
	oldErr := os.Stderr
	os.Stderr = w
	code := run([]string{"demo", "--seed", "42", "--corrupt", "edit-overrun", "--workdir", t.TempDir()})
	w.Close()
	os.Stderr = oldErr
	if code != 2 {
		t.Fatalf("exit=%d want 2", code)
	}
}

func TestBadArgsExitOne(t *testing.T) {
	cases := [][]string{
		{"demo"},
		{"demo", "--seed", "42", "--target-bitrate", "0"},
		{"demo", "--seed", "42", "--corrupt", "not-a-corruption"},
		{"nonsense"},
		{},
	}
	for _, args := range cases {
		if code := run(args); code != 1 {
			t.Fatalf("args %v: exit=%d want 1", args, code)
		}
	}
}
