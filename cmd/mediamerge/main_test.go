package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

type cliSummary struct {
	Segments            int         `json:"segments"`
	SilenceMs           int64       `json:"silence_ms"`
	Duplicates          int         `json:"duplicates"`
	TargetBitrateBps    int64       `json:"target_bitrate_bps"`
	ActualBitrateBps    int64       `json:"actual_bitrate_bps"`
	BitrateDeviationPct float64     `json:"bitrate_deviation_pct"`
	QualitySamples      int64       `json:"quality_samples"`
	AlignmentErrorMs    float64     `json:"manifest_alignment_error_ms"`
	RecoveredTracks     []uint32    `json:"recovered_tracks"`
	IsolatedTracks      []uint32    `json:"isolated_tracks"`
	ManifestSHA256      string      `json:"manifest_sha256"`
	Paths               []pathEntry `json:"paths"`
}

type pathEntry struct {
	Name     string `json:"name"`
	ExitCode int    `json:"exit_code"`
}

func run(t *testing.T, args ...string) (int, cliSummary, []byte) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code := runDemo(args, &out, &errBuf)
	var sum cliSummary
	if code == 0 {
		if err := json.Unmarshal(out.Bytes(), &sum); err != nil {
			t.Fatalf("summary not JSON: %v\n%s", err, out.String())
		}
	}
	return code, sum, out.Bytes()
}

func TestAcceptanceCommand(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	code, sum, _ := run(t, "--seed", "17", "--target-bitrate", "640000",
		"--segment-ms", "2000", "--fixtures", dir, "--manifest", manifest)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if sum.Segments < 4 || sum.Segments > 20 {
		t.Fatalf("segments=%d", sum.Segments)
	}
	if sum.SilenceMs < 0 || sum.SilenceMs > 8000 {
		t.Fatalf("silence_ms=%d", sum.SilenceMs)
	}
	if sum.Duplicates < 0 || sum.Duplicates > 5 {
		t.Fatalf("duplicates=%d", sum.Duplicates)
	}
	if sum.TargetBitrateBps != 640000 {
		t.Fatalf("target=%d", sum.TargetBitrateBps)
	}
	if lo, hi := int64(float64(640000)*0.9), int64(float64(640000)*1.1); sum.ActualBitrateBps < lo || sum.ActualBitrateBps > hi {
		t.Fatalf("actual=%d outside 10%% of target", sum.ActualBitrateBps)
	}
	if sum.BitrateDeviationPct > 10 || sum.BitrateDeviationPct < -10 {
		t.Fatalf("deviation=%v", sum.BitrateDeviationPct)
	}
	if sum.QualitySamples <= 0 {
		t.Fatal("quality_samples <= 0")
	}
	if sum.AlignmentErrorMs > 20 {
		t.Fatalf("alignment=%v", sum.AlignmentErrorMs)
	}
	if len(sum.RecoveredTracks) < 1 || len(sum.IsolatedTracks) < 1 {
		t.Fatalf("recovered=%v isolated=%v", sum.RecoveredTracks, sum.IsolatedTracks)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(sum.ManifestSHA256) {
		t.Fatalf("hash=%q", sum.ManifestSHA256)
	}
	want := map[string]int{"normal": 0, "damaged": 2, "recovered": 0}
	if len(sum.Paths) != len(want) {
		t.Fatalf("paths=%v", sum.Paths)
	}
	for _, p := range sum.Paths {
		if p.ExitCode != want[p.Name] {
			t.Fatalf("%s exit=%d", p.Name, p.ExitCode)
		}
	}
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fingerprints.json")); err != nil {
		t.Fatalf("index missing: %v", err)
	}
}

func TestRerunIsByteIdentical(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	first := filepath.Join(dir, "first.json")
	runArgs := []string{"--seed", "17", "--target-bitrate", "640000",
		"--segment-ms", "2000", "--fixtures", dir, "--manifest", manifest}
	if code, _, _ := run(t, runArgs...); code != 0 {
		t.Fatal("first run failed")
	}
	data1, _ := os.ReadFile(manifest)
	if err := os.Rename(manifest, first); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := run(t, runArgs...); code != 0 {
		t.Fatal("second run failed")
	}
	data2, _ := os.ReadFile(manifest)
	if !bytes.Equal(data1, data2) {
		t.Fatal("manifest differs between identical reruns")
	}
}

func TestBadArgumentsExitOne(t *testing.T) {
	for _, args := range [][]string{
		{"--target-bitrate", "0"},
		{"--segment-ms", "-1"},
		{"--seed", "-2"},
		{"--nope"},
	} {
		dir := t.TempDir()
		full := append([]string{"--fixtures", dir, "--manifest", filepath.Join(dir, "m.json")}, args...)
		if code, _, _ := run(t, full...); code != 1 {
			t.Fatalf("%v: exit=%d", args, code)
		}
	}
}
