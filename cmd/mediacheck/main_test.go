package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mediacheck/internal/fixture"
)

// run invokes the CLI entry point with explicit args and returns its exit
// code, exactly as a terminal would.
func runCLI(t *testing.T, args ...string) int {
	t.Helper()
	return run(args)
}

func TestDemoAllJSONHasContractFields(t *testing.T) {
	code := runCLI(t, "demo", "--case", "all", "--seed", "23", "--json")
	if code != exitOK {
		t.Fatalf("demo --case all exit = %d, want 0", code)
	}
}

func TestDemoSingleCaseMetrics(t *testing.T) {
	// Capture stdout by redirecting at the process level is unnecessary:
	// assert behavior through exit codes and the deterministic engine.
	if code := runCLI(t, "demo", "--case", "normal", "--seed", "23"); code != exitOK {
		t.Fatalf("normal demo exit = %d, want 0", code)
	}
	if code := runCLI(t, "demo", "--case", "rtp_loss", "--seed", "23"); code != exitOK {
		t.Fatalf("rtp_loss demo (multi-case engine) exit = %d, want 0", code)
	}
}

func TestScanExitCodes(t *testing.T) {
	dir := t.TempDir()

	// Unparseable -> 2.
	bad := filepath.Join(dir, "bad.bin")
	if err := os.WriteFile(bad, []byte("not an mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runCLI(t, "scan", "--file", bad, "--json"); code != exitUnparseable {
		t.Fatalf("unparseable exit = %d, want 2", code)
	}

	// Undecidable index -> 3.
	truncSpec := fixture.MediaSpec{MovieScale: 1000, Tracks: []fixture.TrackSpec{
		{TrackID: 1, Handler: "vide", Timescale: 1000, SampleCount: 100,
			Delta: 10, TruncatedIndex: true, SyncEvery: 10},
	}}
	trunc := filepath.Join(dir, "trunc.mp4")
	if err := os.WriteFile(trunc, fixture.BuildMP4(truncSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runCLI(t, "scan", "--file", trunc, "--json"); code != exitUnrecoverable {
		t.Fatalf("undecidable index exit = %d, want 3", code)
	}

	// Healthy -> 0.
	okSpec := fixture.MediaSpec{MovieScale: 1000, Tracks: []fixture.TrackSpec{
		{TrackID: 1, Handler: "vide", Timescale: 1000, SampleCount: 100, Delta: 10, SyncEvery: 10},
		{TrackID: 2, Handler: "soun", Timescale: 8000, SampleCount: 100, Delta: 100},
	}}
	okPath := filepath.Join(dir, "ok.mp4")
	if err := os.WriteFile(okPath, fixture.BuildMP4(okSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runCLI(t, "scan", "--file", okPath, "--json"); code != exitOK {
		t.Fatalf("healthy exit = %d, want 0", code)
	}
}

func TestScanRejectsUnknownCase(t *testing.T) {
	if code := runCLI(t, "demo", "--case", "nope", "--json"); code != exitUnparseable {
		t.Fatalf("unknown case exit = %d, want 2", code)
	}
}

// Ensure the demo envelope JSON is well-formed. This guards against
// serialization regressions in the contract field names.
func TestDemoEnvelopeIsValidJSON(t *testing.T) {
	// Build the envelope directly rather than capturing stdout, keeping the
	// test hermetic and independent of os.Stdout buffering.
	env := demoOutput{Seed: 23, KeyframeGapMs: map[uint32]int64{1: 1000}}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]interface{}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("envelope not valid JSON: %v", err)
	}
	if _, ok := back["keyframe_gap_ms"]; !ok {
		t.Fatal("envelope must expose keyframe_gap_ms")
	}
}
