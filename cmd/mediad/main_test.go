package main

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// buildDemo compiles the CLI once for the end-to-end tests.
func buildDemo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "mediad")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// execDemo executes the demo with the acceptance arguments and returns stdout.
func execDemo(t *testing.T, bin, genDir string, extra ...string) (string, int) {
	t.Helper()
	args := append([]string{
		"demo",
		"--seed", "41",
		"--input", genDir,
		"--rate", "48000",
		"--layout", "stereo",
		"--loudness", "-18",
		"--target-kbps", "128",
	}, extra...)
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run: %v\n%s", err, out)
		}
	}
	return string(out), code
}

func TestCLIDemoAcceptance(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end demo is slow")
	}
	bin := buildDemo(t)
	gen := filepath.Join(t.TempDir(), "generated")
	out, code := execDemo(t, bin, gen)
	if code != 0 {
		t.Fatalf("demo exit %d\n%s", code, out)
	}
	required := []string{
		"target_kbps=128",
		"sample_rate=48000",
		"layout=stereo",
		"actual_kbps=",
		"integrated_lufs=",
		"true_peak_dbtp=",
		"quality_clipped_samples=",
		"timeline_error_ticks=",
		"recovered_samples=",
		"missing_duration_ms=",
		"manifest_alignment_ms=",
		"output_sha256=",
	}
	for _, r := range required {
		if !strings.Contains(out, r) {
			t.Fatalf("demo output missing %q\n%s", r, out)
		}
	}
	// Field-level acceptance bounds, parsed from the printed lines.
	fields := parseFields(t, out)
	if fields["sample_rate"] != "48000" {
		t.Fatalf("sample_rate %s", fields["sample_rate"])
	}
	if fields["layout"] != "stereo" {
		t.Fatalf("layout %s", fields["layout"])
	}
	if fields["quality_clipped_samples"] != "0" {
		t.Fatalf("clipped %s", fields["quality_clipped_samples"])
	}
	if hash := fields["output_sha256"]; len(hash) != 64 {
		t.Fatalf("hash length %d", len(hash))
	}
	// Target deviation must be within 5 %.
	target := mustFloat(t, fields["target_kbps"])
	actual := mustFloat(t, fields["actual_kbps"])
	dev := absf((actual - target) / target * 100)
	if dev > 5 {
		t.Fatalf("actual_kbps %.2f deviates %.2f%%", actual, dev)
	}
	// Loudness and peak bounds.
	lufs := mustFloat(t, fields["integrated_lufs"])
	if absf(lufs-(-18)) > 1.5 {
		t.Fatalf("integrated_lufs %.2f", lufs)
	}
	if peak := mustFloat(t, fields["true_peak_dbtp"]); peak > -1.0 {
		t.Fatalf("true_peak_dbtp %.2f", peak)
	}
	if ticks := mustFloat(t, fields["timeline_error_ticks"]); ticks > 1 {
		t.Fatalf("timeline_error_ticks %.0f", ticks)
	}
	if rec := mustFloat(t, fields["recovered_samples"]); rec <= 0 {
		t.Fatalf("recovered_samples %.0f", rec)
	}
	if missing := mustFloat(t, fields["missing_duration_ms"]); missing <= 0 {
		t.Fatalf("missing_duration_ms %.0f", missing)
	}
	if align := mustFloat(t, fields["manifest_alignment_ms"]); align > 1 {
		t.Fatalf("manifest_alignment_ms %.0f", align)
	}
}

func TestCLIRepeatHashMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end repeat is slow")
	}
	bin := buildDemo(t)
	gen := filepath.Join(t.TempDir(), "generated")
	out, code := execDemo(t, bin, gen, "--repeat")
	if code != 0 {
		t.Fatalf("repeat exit %d\n%s", code, out)
	}
	fields := parseFields(t, out)
	if fields["repeat_sha256_match"] != "true" {
		t.Fatalf("repeat_sha256_match=%s\n%s", fields["repeat_sha256_match"], out)
	}
}

func TestCLIBadFlagIsUsage(t *testing.T) {
	bin := buildDemo(t)
	cmd := exec.Command(bin, "demo", "--layout", "surround")
	_, err := cmd.CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 2 {
		t.Fatalf("expected exit 2, got %v", err)
	}
}

func TestCLIUnknownCommandIsUsage(t *testing.T) {
	bin := buildDemo(t)
	cmd := exec.Command(bin, "bogus")
	_, err := cmd.CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 2 {
		t.Fatalf("expected exit 2, got %v", err)
	}
}

// parseFields extracts key=value lines, skipping fault and summary lines.
func parseFields(t *testing.T, out string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "fault:") || strings.HasPrefix(line, "exit status") {
			continue
		}
		if i := strings.IndexByte(line, '='); i > 0 {
			m[line[:i]] = line[i+1:]
		}
	}
	return m
}

func mustFloat(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
