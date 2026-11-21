package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI invokes the command in-process and captures both streams.
func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestSynthThenAnalyzePasses(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "demo.wav")
	code, out, errOut := runCLI(t, "synth", "--out", wav, "--layout", "5.1",
		"--seconds", "4", "--tone-dbfs", "-27", "--target-lufs", "-23")
	if code != 0 {
		t.Fatalf("synth exit = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "result=PASS") {
		t.Errorf("synth verification did not pass:\n%s", out)
	}
	if _, err := os.Stat(wav); err != nil {
		t.Fatalf("synth did not produce a file: %v", err)
	}

	code, out, _ = runCLI(t, "analyze", wav, "--target-lufs", "-23",
		"--tolerance-lu", "1", "--max-true-peak-dbtp", "-1")
	if code != 0 {
		t.Fatalf("analyze exit = %d\n%s", code, out)
	}
	for _, want := range []string{"integrated_lufs=-22.", "short_term_peak_lufs=-22.",
		"true_peak_dbtp=-27.00", "integrated_pass=true", "true_peak_pass=true", "result=PASS"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestBatchDemoScenario(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "demo.wav")
	if code, _, errOut := runCLI(t, "synth", "--out", wav, "--layout", "5.1",
		"--seconds", "12", "--tone-dbfs", "-27"); code != 0 {
		t.Fatalf("synth failed: %s", errOut)
	}
	code, out, _ := runCLI(t, "batch", "--input-dir", dir, "--workers", "4",
		"--target-lufs", "-23", "--tolerance-lu", "1", "--max-true-peak-dbtp", "-1",
		"--format", "text")
	if code != 0 {
		t.Fatalf("batch exit = %d\n%s", code, out)
	}
	for _, want := range []string{"files=1", "integrated_lufs=-22.", "short_term_peak_lufs=-22.",
		"true_peak_dbtp=-27.00", "integrated_pass=true", "true_peak_pass=true", "result=PASS"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestThresholdMissExitsOne(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "quiet.wav")
	// The requested level is deliberately outside the default delivery
	// window, so synth's own verification reports a threshold failure; the
	// file is still written and analyse it explicitly below.
	runCLI(t, "synth", "--out", wav, "--layout", "stereo",
		"--seconds", "4", "--tone-dbfs", "-40")
	code, out, _ := runCLI(t, "analyze", wav, "--target-lufs", "-23", "--tolerance-lu", "1")
	if code != 1 {
		t.Fatalf("exit = %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "integrated_pass=false") || !strings.Contains(out, "reason=") {
		t.Errorf("expected an explained failure:\n%s", out)
	}
}

func TestTruePeakCeilingExitsOne(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "hot.wav")
	// -0.2 dBFS tone implies a true peak above -1 dBTP if interpolation adds
	// anything, and the sample peak alone is already above the ceiling.
	if code, _, _ := runCLI(t, "synth", "--out", wav, "--seconds", "4",
		"--tone-dbfs", "-0.2", "--tone-hz", "997", "--max-true-peak-dbtp", "6",
		"--target-lufs", "-3", "--tolerance-lu", "3"); code != 0 {
		t.Fatalf("synth exit = %d", code)
	}
	code, out, _ := runCLI(t, "analyze", wav, "--target-lufs", "-3", "--tolerance-lu", "2",
		"--max-true-peak-dbtp", "-1")
	if code != 1 {
		t.Fatalf("exit = %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "true_peak_pass=false") {
		t.Errorf("expected true-peak failure:\n%s", out)
	}
}

func TestCorruptFileExitsThree(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.wav")
	if code, _, _ := runCLI(t, "synth", "--out", good, "--seconds", "4", "--tone-dbfs", "-23"); code != 0 {
		t.Fatalf("synth failed")
	}
	bad := filepath.Join(dir, "bad.wav")
	if err := os.WriteFile(bad, []byte("RIFFxxxxWAVEfmt "), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCLI(t, "batch", good, bad, "--target-lufs", "-26", "--tolerance-lu", "1")
	if code != 3 {
		t.Fatalf("exit = %d, want 3\n%s", code, out)
	}
	if !strings.Contains(out, "errors=1") {
		t.Errorf("batch summary should count the error:\n%s", out)
	}
	if !strings.Contains(out, filepath.Base(good)) {
		t.Errorf("healthy file was dropped from the report:\n%s", out)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"frobnicate"},
		{"analyze"},
		{"analyze", "a.wav", "b.wav"},
		{"batch"},
		{"analyze", "does-not-exist.wav"},
	} {
		code, _, errOut := runCLI(t, args...)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2 (stderr %q)", args, code, errOut)
		}
	}
}

func TestJSONBatchOutput(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "demo.wav")
	if code, _, _ := runCLI(t, "synth", "--out", wav, "--seconds", "4", "--tone-dbfs", "-23"); code != 0 {
		t.Fatalf("synth failed")
	}
	// The synth tone measures about -23 LUFS; ask synth to verify against that
	// so it exits cleanly, then batch with the same target.
	code, out, errOut := runCLI(t, "batch", wav, "--format", "json", "--target-lufs", "-23")
	if code != 0 {
		t.Fatalf("exit = %d\n%s\n%s", code, out, errOut)
	}
	var payload struct {
		Files   int    `json:"files"`
		Result  string `json:"result"`
		Results []struct {
			Path           string   `json:"path"`
			IntegratedLUFS *float64 `json:"integrated_lufs"`
			TruePeakDBTP   *float64 `json:"true_peak_dbtp"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if payload.Files != 1 || payload.Result != "PASS" || len(payload.Results) != 1 {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Results[0].IntegratedLUFS == nil || payload.Results[0].TruePeakDBTP == nil {
		t.Error("JSON omitted measured values")
	}
}

func TestHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"help"}, {"analyze", "--help"}} {
		code, out, _ := runCLI(t, args...)
		if code != 0 {
			t.Errorf("%v: exit = %d, want 0", args, code)
		}
		if !strings.Contains(out, "usage:") {
			t.Errorf("%v: help text missing from stdout:\n%s", args, out)
		}
	}
}

func TestSynthDoesNotOverwriteWithoutRenaming(t *testing.T) {
	// synth writes atomically via a temporary file; no .tmp should be left.
	dir := t.TempDir()
	wav := filepath.Join(dir, "out.wav")
	if code, _, _ := runCLI(t, "synth", "--out", wav, "--seconds", "1"); code != 0 {
		t.Fatal("synth failed")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestAnalyzeSilenceIsNotAPass(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "silence.wav")
	if code, _, _ := runCLI(t, "synth", "--out", wav, "--seconds", "4", "--tone-dbfs", "-999"); code != 0 {
		// Synth's own verification will fail the thresholds; only the file
		// itself matters here.
		t.Logf("synth on silence exited non-zero (expected when verifying)")
	}
	code, out, _ := runCLI(t, "analyze", wav, "--target-lufs", "-23", "--tolerance-lu", "1")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for silence\n%s", code, out)
	}
	if !strings.Contains(out, "integrated_lufs=n/a") || !strings.Contains(out, "integrated_valid=false") {
		t.Errorf("silence should be reported as unmeasurable:\n%s", out)
	}
	if strings.Contains(out, "result=PASS") {
		t.Errorf("silence must never pass:\n%s", out)
	}
}
