package main

import (
	"bytes"
	"encoding/json"
	"os"
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

func TestAnalyzeDemoRequiredKeys(t *testing.T) {
	dir := t.TempDir()
	code, out, errs := runCLI(t, "analyze", "--fixture", "generated",
		"--workdir", dir, "--fps", "30000/1001", "--workers", "4", "--queue", "8", "--mode", "recover")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errs)
	}
	for _, key := range []string{
		"target_bitrate_bps", "actual_bitrate_bps", "bitrate_deviation_pct",
		"cue_count", "damage_count", "keyframe_gap_max_ms", "drift_ppm",
		"manifest_alignment_error_ms", "manifest_sha256",
	} {
		if !strings.Contains(out, key+"=") {
			t.Fatalf("missing %s in output:\n%s", key, out)
		}
	}
	if !strings.Contains(out, "damage_count=") {
		t.Fatal("no damage count")
	}
	// Order-of-magnitude acceptance checks from the contract.
	var dev float64
	var cues, damage int
	var gap, align float64
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "bitrate_deviation_pct":
			dev = mustFloat(t, v)
		case "cue_count":
			cues = mustInt(t, v)
		case "damage_count":
			damage = mustInt(t, v)
		case "keyframe_gap_max_ms":
			gap = mustFloat(t, v)
		case "manifest_alignment_error_ms":
			align = mustFloat(t, v)
		}
	}
	if dev > 5 || dev < -5 {
		t.Fatalf("bitrate deviation %.3f%% outside ±5%%", dev)
	}
	if cues < 10 || damage < 1 {
		t.Fatalf("cues=%d damage=%d", cues, damage)
	}
	if gap < 100 || gap > 5000 {
		t.Fatalf("keyframe gap = %v ms", gap)
	}
	if align > 2 {
		t.Fatalf("alignment error = %v ms", align)
	}
}

func TestStrictModeReturnsOneWithOffset(t *testing.T) {
	dir := t.TempDir()
	code, out, errs := runCLI(t, "analyze", "--fixture", "damaged", "--workdir", dir,
		"--fps", "25", "--mode", "strict")
	if code != 1 {
		t.Fatalf("strict exit = %d (%s%s)", code, out, errs)
	}
	if !strings.Contains(errs, "error code=") || !strings.Contains(errs, "offset=") {
		t.Fatalf("strict error lacks offset: %q", errs)
	}
	if strings.Contains(out, "manifest_sha256=") {
		t.Fatalf("strict failure printed a manifest: %s", out)
	}
}

func TestBadArgsReturnTwo(t *testing.T) {
	cases := [][]string{
		{},
		{"bogus"},
		{"analyze", "--mode", "sideways"},
		{"analyze", "--workers", "0"},
		{"analyze", "--queue", "0"},
		{"analyze", "--fixture", "generated", "--bogus"},
		{"analyze"}, // no inputs
	}
	for _, args := range cases {
		if code, out, errs := runCLI(t, args...); code != 2 {
			t.Fatalf("%v exit = %d (%s%s)", args, code, out, errs)
		}
	}
}

func TestRepeatedRunDigestStable(t *testing.T) {
	dir := t.TempDir()
	args := []string{"analyze", "--fixture", "generated", "--workdir", dir,
		"--fps", "30000/1001", "--workers", "4", "--queue", "8", "--mode", "recover"}
	_, out1, _ := runCLI(t, args...)
	_, out2, _ := runCLI(t, args...)
	d1, d2 := digestOf(out1), digestOf(out2)
	if d1 == "" || d1 != d2 {
		t.Fatalf("digests %q vs %q", d1, d2)
	}
}

func digestOf(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "manifest_sha256=") {
			return strings.TrimPrefix(line, "manifest_sha256=")
		}
	}
	return ""
}

func TestManifestFileIsAtomicAndValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "manifest.json")
	code, _, errs := runCLI(t, "analyze", "--fixture", "normal", "--workdir", dir,
		"--fps", "30000/1001", "--mode", "recover", "--manifest", path)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest not JSON: %v", err)
	}
	if m["Digest"] == "" || m["Digest"] == nil {
		t.Fatal("manifest lacks digest")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".manifest-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestExplicitInputGrouping(t *testing.T) {
	dir := t.TempDir()
	code, out, errs := runCLI(t, "generate", "--dir", dir)
	if code != 0 {
		t.Fatalf("generate exit %d: %s", code, errs)
	}
	code, out, errs = runCLI(t, "analyze", "--input", filepath.Join(dir, "normal_2997.mp4"),
		"--input", filepath.Join(dir, "normal_2997.srt"),
		"--fps", "30000/1001", "--mode", "recover")
	if code != 0 {
		t.Fatalf("analyze exit %d: %s %s", code, out, errs)
	}
	if !strings.Contains(out, "assets=1") {
		t.Fatalf("mp4 and srt should group into one asset:\n%s", out)
	}
}

func mustFloat(t *testing.T, s string) float64 {
	t.Helper()
	var f float64
	if _, err := fmtSscan(s, &f); err != nil {
		t.Fatalf("float %q: %v", s, err)
	}
	return f
}

func mustInt(t *testing.T, s string) int {
	t.Helper()
	var n int
	if _, err := fmtSscan(s, &n); err != nil {
		t.Fatalf("int %q: %v", s, err)
	}
	return n
}
