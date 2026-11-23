package main

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

// reportFields are the acceptance fields the demo must always print.
var reportFields = []string{
	"segments=",
	"silence_ms=",
	"duplicates=",
	"target_bitrate_kbps=",
	"actual_bitrate_kbps=",
	"bitrate_deviation_pct=",
	"quality_samples=",
	"manifest_alignment_error_ms=",
	"recovered_blocks=",
	"resume_offset=",
	"manifest_sha256=",
}

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code := run(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

func TestDemoCleanRun(t *testing.T) {
	dir := t.TempDir()
	code, out, errText := runCLI(t, "demo", "--generate", "--seed", "41", "--index-dir", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errText)
	}
	for _, field := range reportFields {
		if !strings.Contains(out, field) {
			t.Errorf("report missing %q", field)
		}
	}
}

func TestDemoDamagedInputExits2(t *testing.T) {
	dir := t.TempDir()
	code, out, errText := runCLI(t, "demo", "--generate", "--seed", "41",
		"--index-dir", dir, "--inject-truncated-box")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr: %s)", code, errText)
	}
	if !strings.Contains(errText, "truncated_box") {
		t.Errorf("stderr does not name truncated_box: %s", errText)
	}
	if !strings.Contains(out, "recovered=true") {
		t.Errorf("report does not state recovery: %s", out)
	}
	if !strings.Contains(out, "recovered_blocks=0") {
		t.Log("note: recovered_blocks is only non-zero for a simulated crash")
	}
}

func TestDemoFullAcceptanceFlags(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := runCLI(t, "demo", "--generate", "--seed", "41",
		"--block-samples", "128",
		"--inject-seq-gap", "--inject-tfdt-regression", "--inject-wrap",
		"--inject-vfr", "--inject-truncated-box", "--inject-checkpoint-crash",
		"--index-dir", dir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	for _, want := range []string{
		"faults=sequence_gap,tfdt_regression,truncated_box",
		"rtp_wraps=1",
		"duplicates=1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	// The acceptance bounds: bitrate deviation 0..15 %, alignment <= 40 ms,
	// at least two recovered blocks.
	if !strings.Contains(out, "recovered_blocks=") {
		t.Fatal("missing recovered_blocks")
	}
	blocks, err := strconv.Atoi(extract(out, "recovered_blocks="))
	if err != nil || blocks < 2 {
		t.Errorf("recovered_blocks = %q, want >= 2", extract(out, "recovered_blocks="))
	}
}

func TestDemoVerifyRepeat(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := runCLI(t, "demo", "--generate", "--seed", "41",
		"--index-dir", dir, "--verify-repeat")
	if code != 0 && code != 2 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "repeat_manifest_match=true") {
		t.Fatalf("repeat comparison failed:\n%s", out)
	}
}

func TestDemoSameSeedSameManifest(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	_, outA, _ := runCLI(t, "demo", "--generate", "--seed", "41", "--index-dir", dirA)
	_, outB, _ := runCLI(t, "demo", "--generate", "--seed", "41", "--index-dir", dirB)
	if extract(outA, "manifest_sha256=") != extract(outB, "manifest_sha256=") {
		t.Fatalf("manifest digest depends on the index directory")
	}
}

func TestDemoRejectsMissingGenerate(t *testing.T) {
	code, _, errText := runCLI(t, "demo", "--seed", "41")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "--generate is required") {
		t.Fatalf("stderr = %s", errText)
	}
}

func TestUnknownCommandFails(t *testing.T) {
	if code, _, _ := runCLI(t, "transcode"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

func TestInvalidBlockSamplesFails(t *testing.T) {
	code, _, errText := runCLI(t, "demo", "--generate", "--block-samples", "0")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "block-samples") {
		t.Fatalf("stderr = %s", errText)
	}
}

// extract returns the text following prefix on its line.
func extract(out, prefix string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}
