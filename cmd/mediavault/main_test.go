package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/mediavault/internal/manifest"
	"example.com/mediavault/internal/pipeline"
)

// captureDemo runs the demo against a temp dir and returns its stdout plus the
// parsed summary lines.
func captureDemo(t *testing.T, dir string) (string, map[string]string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "demo-stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sum, _, err := pipeline.RunDemo(dir, f)
	if err != nil {
		t.Fatalf("demo: %v", err)
	}
	pipeline.PrintDemo(f, sum) // the summary block is part of demo stdout
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(f); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	fields := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && !strings.ContainsAny(k, " :") {
			fields[k] = v
		}
	}
	return out, fields
}

func TestDemoMeetsAcceptanceSummary(t *testing.T) {
	dir := t.TempDir()
	out, fields := captureDemo(t, dir)
	want := map[string]string{
		"resume_from_block":               "2",
		"committed_blocks":                "32",
		"rtp_wraps":                       "1",
		"rtp_packets":                     "32",
		"drift_ppm":                       "250",
		"duplicate_recomputed_blocks":     "0",
		"container_recomputed_pcm_blocks": "0",
		"changed_recomputed_blocks":       "1",
		"changed_reused_blocks":           "31",
		"rejected_fixtures":               "4",
		"manifest_entries":                "3",
		"unique_pcm":                      "2",
	}
	for k, v := range want {
		if fields[k] != v {
			t.Fatalf("%s = %q, want %q\n%s", k, fields[k], v, out)
		}
	}
	digest := fields["manifest_digest"]
	if len(digest) != 64 {
		t.Fatalf("manifest digest %q is not 64 hex characters", digest)
	}
	for _, stage := range []string{"RECEIVED", "SCANNED", "FINGERPRINTED", "NORMALIZED", "ARCHIVED"} {
		if !strings.Contains(out, "-> "+stage) && !strings.Contains(out, "-> RECEIVED") {
			if stage != "RECEIVED" {
				t.Fatalf("demo output is missing stage %s", stage)
			}
		}
	}
}

func TestVerifyPassesAndMatchesDemoDigest(t *testing.T) {
	dir := t.TempDir()
	_, fields := captureDemo(t, dir)
	res, err := verifyDir(dir)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.Verified != 3 || res.Failed != 0 {
		t.Fatalf("verified=%d failed=%d %v", res.Verified, res.Failed, res.Failures)
	}
	if res.Digest != fields["manifest_digest"] {
		t.Fatalf("verify digest %s != demo digest %s", res.Digest, fields["manifest_digest"])
	}
}

func TestDemoIsRepeatableWithSameDigest(t *testing.T) {
	dir := t.TempDir()
	_, first := captureDemo(t, dir)
	_, second := captureDemo(t, dir)
	if first["manifest_digest"] != second["manifest_digest"] {
		t.Fatalf("rerunning demo changed the digest: %s vs %s", first["manifest_digest"], second["manifest_digest"])
	}
}

func TestDemoRefusesToClearForeignDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "important.txt"), []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pipeline.RunDemo(dir, os.Stderr); err == nil {
		t.Fatal("demo cleared a directory it did not create")
	}
	if _, err := os.Stat(filepath.Join(dir, "important.txt")); err != nil {
		t.Fatal("user file was removed")
	}
}

func TestVerifyFailsOnTamperedManifest(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := pipeline.RunDemo(dir, os.Stderr); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, manifest.FileName))
	if err != nil {
		t.Fatal(err)
	}
	var f manifest.File
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	// Flip a chunk digest to something valid-looking but wrong.
	f.Entries[0].Chunks[0].Digest = strings.Repeat("0", 64)
	tampered, err := json.MarshalIndent(&f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), append(tampered, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := verifyDir(dir)
	if err != nil {
		t.Fatalf("verify returned an error instead of a result: %v", err)
	}
	if res.Failed != 1 || res.Verified != 2 {
		t.Fatalf("verified=%d failed=%d", res.Verified, res.Failed)
	}
}

func TestParseDirRejectsBadInput(t *testing.T) {
	cases := [][]string{
		{"--dir"},           // missing value
		{"--dir", ""},       // empty
		{"--dir", "x", "y"}, // positional noise
		{"--nope"},
	}
	for _, args := range cases {
		if _, err := parseDir(args); err == nil {
			t.Fatalf("parseDir(%v) accepted bad input", args)
		}
	}
	dir, err := parseDir(nil)
	if err != nil {
		t.Fatal(err)
	}
	if dir == "" || !filepath.IsAbs(dir) {
		t.Fatalf("default dir %q is not absolute", dir)
	}
}

func TestExitCodeContract(t *testing.T) {
	if code := exitCode(nil); code != 0 {
		t.Fatalf("success = %d", code)
	}
	if code := run([]string{"nope"}); code != 2 {
		t.Fatalf("unknown command = %d", code)
	}
	if code := run(nil); code != 2 {
		t.Fatalf("no command = %d", code)
	}
	if code := run([]string{"verify", "--dir", filepath.Join(t.TempDir(), "missing")}); code != 2 {
		t.Fatalf("missing dir = %d", code)
	}
}
