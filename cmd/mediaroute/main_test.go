package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI executes a subcommand in-process and returns stdout, stderr and the
// error. Using run directly keeps tests fast and lets us assert exit mapping.
func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errBuf bytes.Buffer
	err := run(args, &out, &errBuf)
	return out.String(), errBuf.String(), err
}

func TestDemoProducesAllCases(t *testing.T) {
	dir := t.TempDir()
	stdout, _, err := runCLI(t, "demo", "--seed", "17", "--queue", "2",
		"--target-bps", "2000000", "--out-dir", dir)
	if err != nil {
		t.Fatalf("demo failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 5 {
		t.Fatalf("want 5 case records, got %d", len(lines))
	}
	seen := map[string]bool{}
	for _, line := range lines {
		var rec demoCase
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad JSON record: %v\n%s", err, line)
		}
		if !rec.OK {
			t.Fatalf("case %s failed: %s", rec.Case, rec.Error)
		}
		seen[rec.Case] = true
		if rec.SHA256 == "" || len(rec.SHA256) != 64 {
			t.Fatalf("case %s has bad sha256 %q", rec.Case, rec.SHA256)
		}
		if rec.ManifestAlignmentUS > 1 {
			t.Fatalf("case %s alignment %d us > 1 us", rec.Case, rec.ManifestAlignmentUS)
		}
		if _, err := os.Stat(rec.OutputPath); err != nil {
			t.Fatalf("case %s did not write media: %v", rec.Case, err)
		}
		if _, err := os.Stat(rec.ManifestPath); err != nil {
			t.Fatalf("case %s did not write manifest: %v", rec.Case, err)
		}
	}
	for _, want := range []string{"clean", "sequence-gap", "tfdt-regression", "truncated-box", "corrupt-index"} {
		if !seen[want] {
			t.Fatalf("missing demo case %q", want)
		}
	}
}

func TestDemoAcceptanceCriteria(t *testing.T) {
	dir := t.TempDir()
	stdout, _, err := runCLI(t, "demo", "--seed", "17", "--queue", "2",
		"--target-bps", "2000000", "--out-dir", dir)
	if err != nil {
		t.Fatalf("demo failed: %v", err)
	}
	byCase := map[string]demoCase{}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var rec demoCase
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		byCase[rec.Case] = rec
	}
	if c := byCase["clean"]; c.ValidSamplePct != 100 || c.MFHDGaps != 0 {
		t.Fatalf("clean case: valid=%v gaps=%d", c.ValidSamplePct, c.MFHDGaps)
	}
	if c := byCase["sequence-gap"]; c.MFHDGaps != 1 {
		t.Fatalf("gap case: gaps=%d want 1", c.MFHDGaps)
	}
	if c := byCase["tfdt-regression"]; c.RepairedTfdt < 1 || !c.Monotonic || c.ManifestAlignmentUS > 1 {
		t.Fatalf("regression case failed acceptance: %+v", c)
	}
	if c := byCase["truncated-box"]; c.TruncatedBoxes != 1 || c.RecoveredFragments < 1 {
		t.Fatalf("truncated case failed acceptance: %+v", c)
	}
	for name, c := range byCase {
		if abs(c.BitrateDeltaPct) > 5 {
			t.Fatalf("%s bitrate delta %v%% exceeds 5%%", name, c.BitrateDeltaPct)
		}
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func TestDemoIsReproducible(t *testing.T) {
	dir := t.TempDir()
	a, _, err := runCLI(t, "demo", "--seed", "17", "--queue", "2", "--target-bps", "2000000", "--out-dir", dir)
	if err != nil {
		t.Fatalf("first demo: %v", err)
	}
	aFiles := snapshotDigests(t, dir)
	b, _, err := runCLI(t, "demo", "--seed", "17", "--queue", "2", "--target-bps", "2000000", "--out-dir", dir)
	if err != nil {
		t.Fatalf("second demo: %v", err)
	}
	bFiles := snapshotDigests(t, dir)
	if a != b {
		t.Fatal("demo JSON output differs between runs on identical input")
	}
	if len(aFiles) != len(bFiles) {
		t.Fatal("artifact count differs between runs")
	}
	for name, digest := range aFiles {
		if bFiles[name] != digest {
			t.Fatalf("artifact %s changed between runs", name)
		}
	}
}

func snapshotDigests(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = modelDigest(b)
	}
	return out
}

func TestVerifySucceedsForAllCases(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runCLI(t, "demo", "--seed", "17", "--queue", "2", "--target-bps", "2000000", "--out-dir", dir); err != nil {
		t.Fatalf("demo failed: %v", err)
	}
	for _, name := range []string{"demo-output", "demo-sequence-gap", "demo-tfdt-regression", "demo-truncated-box", "demo-corrupt-index"} {
		media := filepath.Join(dir, name+".mp4")
		manifest := filepath.Join(dir, name+"-manifest.json")
		if name == "demo-output" {
			manifest = filepath.Join(dir, "demo-manifest.json")
		}
		stdout, _, err := runCLI(t, "verify", "--input", media, "--manifest", manifest)
		if err != nil {
			t.Fatalf("verify %s failed: %v\n%s", name, err, stdout)
		}
		var rec verifyRecord
		if err := json.Unmarshal([]byte(stdout), &rec); err != nil {
			t.Fatalf("bad verify JSON: %v", err)
		}
		if !rec.OK || !rec.SHA256Match {
			t.Fatalf("verify %s did not pass: %+v", name, rec)
		}
		if rec.IndexValidEntries != rec.IndexTotalEntries || rec.IndexTotalEntries == 0 {
			t.Fatalf("verify %s: %d/%d valid entries", name, rec.IndexValidEntries, rec.IndexTotalEntries)
		}
		if rec.MaxAlignmentUS > 1 {
			t.Fatalf("verify %s: alignment %d us", name, rec.MaxAlignmentUS)
		}
	}
}

func TestVerifyFailsOnTamperedMedia(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runCLI(t, "demo", "--seed", "17", "--out-dir", dir); err != nil {
		t.Fatalf("demo failed: %v", err)
	}
	media := filepath.Join(dir, "demo-output.mp4")
	raw, err := os.ReadFile(media)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 0xFF
	if err := os.WriteFile(media, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err = runCLI(t, "verify", "--input", media,
		"--manifest", filepath.Join(dir, "demo-manifest.json"))
	if err == nil {
		t.Fatal("expected verification to fail for tampered media")
	}
	if code := exitCode(err); code == 0 {
		t.Fatal("tampered media must produce a nonzero exit code")
	}
}

func TestVerifyFailsOnManifestMismatch(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runCLI(t, "demo", "--seed", "17", "--out-dir", dir); err != nil {
		t.Fatalf("demo failed: %v", err)
	}
	// Verify the clean file against the truncated file's manifest.
	_, _, err := runCLI(t, "verify",
		"--input", filepath.Join(dir, "demo-output.mp4"),
		"--manifest", filepath.Join(dir, "demo-truncated-box-manifest.json"))
	if err == nil {
		t.Fatal("expected manifest mismatch to fail")
	}
	if code := exitCode(err); code == 0 {
		t.Fatal("manifest mismatch must produce a nonzero exit code")
	}
}

func TestVerifyRejectsMissingInput(t *testing.T) {
	_, _, err := runCLI(t, "verify", "--input", filepath.Join(t.TempDir(), "nope.mp4"),
		"--manifest", "nope.json")
	if err == nil {
		t.Fatal("expected failure for missing input")
	}
	if code := exitCode(err); code == 0 {
		t.Fatal("missing input must be nonzero")
	}
}

func TestSafeOutputPathRejectsTraversal(t *testing.T) {
	bad := []string{"../escape.mp4", "a/b.mp4", `a\b.mp4`, "..", ""}
	for _, name := range bad {
		if _, err := safeOutputPath(t.TempDir(), name); err == nil {
			t.Fatalf("expected rejection of %q", name)
		}
	}
	if _, err := safeOutputPath(t.TempDir(), "ok.mp4"); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
	if _, err := safeOutputPath(contentRootForTest()+"/../..", "ok.mp4"); err == nil {
		t.Fatal("expected rejection of escaping out-dir")
	}
}

// contentRootForTest returns a directory that can be made to escape using
// parent references.
func contentRootForTest() string { return "somedir/child" }

func TestUnknownSubcommandFails(t *testing.T) {
	_, _, err := runCLI(t, "frobnicate")
	if err == nil {
		t.Fatal("expected unknown subcommand to fail")
	}
}

func TestDemoRejectsInvalidFlags(t *testing.T) {
	if _, _, err := runCLI(t, "demo", "--queue", "0"); err == nil {
		t.Fatal("expected zero queue to be rejected")
	}
	if _, _, err := runCLI(t, "demo", "--target-bps", "0"); err == nil {
		t.Fatal("expected zero target to be rejected")
	}
}

// modelDigest hashes bytes for the reproducibility snapshot.
func modelDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestVerifyFailsOnIndexArtifactMismatch(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runCLI(t, "demo", "--seed", "17", "--out-dir", dir); err != nil {
		t.Fatalf("demo failed: %v", err)
	}
	// Tamper with a copy of the index artifact's first entry offset.
	raw, err := os.ReadFile(filepath.Join(dir, "demo-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	entries := doc["entries"].([]interface{})
	first := entries[0].(map[string]interface{})
	first["moof_offset"] = float64(1)
	tampered, _ := json.Marshal(doc)
	badIndex := filepath.Join(dir, "bad-index.json")
	if err := os.WriteFile(badIndex, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err = runCLI(t, "verify",
		"--input", filepath.Join(dir, "demo-output.mp4"),
		"--manifest", filepath.Join(dir, "demo-manifest.json"),
		"--index", badIndex)
	if err == nil {
		t.Fatal("expected index artifact mismatch to fail verification")
	}
	if code := exitCode(err); code == 0 {
		t.Fatal("index mismatch must be nonzero")
	}
}

func TestVerifyFailsOnTamperedManifestDigest(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runCLI(t, "demo", "--seed", "17", "--out-dir", dir); err != nil {
		t.Fatalf("demo failed: %v", err)
	}
	manifestPath := filepath.Join(dir, "demo-manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["mfhd_gaps"] = float64(99)
	tampered, _ := json.Marshal(m)
	if err := os.WriteFile(manifestPath, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err = runCLI(t, "verify",
		"--input", filepath.Join(dir, "demo-output.mp4"),
		"--manifest", manifestPath)
	if err == nil {
		t.Fatal("expected manifest self-digest mismatch to fail")
	}
	if code := exitCode(err); code == 0 {
		t.Fatal("tampered manifest must be nonzero")
	}
}

func TestDemoRTPReportPresent(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runCLI(t, "demo", "--seed", "17", "--out-dir", dir); err != nil {
		t.Fatalf("demo failed: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "demo-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		RTP *struct {
			Matched bool `json:"matched"`
			Wraps   int  `json:"wraps"`
		} `json:"rtp"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.RTP == nil {
		t.Fatal("manifest should contain an RTP report")
	}
	if !m.RTP.Matched {
		t.Fatal("RTP report should match the media timeline")
	}
	if m.RTP.Wraps < 1 {
		t.Fatalf("demo should exercise at least one RTP wrap, got %d", m.RTP.Wraps)
	}
}
