package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pairwise/mediad/internal/pipeline"
	"github.com/pairwise/mediad/internal/verify"
)

func runMain(t *testing.T, args ...string) int {
	t.Helper()
	return run(args)
}

func TestUsageAndUnknownCommandExitTwo(t *testing.T) {
	if got := runMain(t); got != 2 {
		t.Fatalf("no args exit=%d", got)
	}
	if got := runMain(t, "bogus"); got != 2 {
		t.Fatalf("unknown command exit=%d", got)
	}
	if got := runMain(t, "demo"); got != 2 {
		t.Fatalf("demo without --out exit=%d", got)
	}
	if got := runMain(t, "verify"); got != 2 {
		t.Fatalf("verify without --dir exit=%d", got)
	}
}

func TestDemoAndVerifyRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	if got := runMain(t, "demo", "--out", dir); got != 0 {
		t.Fatalf("demo exit=%d", got)
	}
	manifest := filepath.Join(dir, "manifest.json")
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if got := runMain(t, "verify", "--dir", dir); got != 0 {
		t.Fatalf("verify exit=%d", got)
	}
}

func TestVerifyRejectsTamperedMedia(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	if got := runMain(t, "demo", "--out", dir); got != 0 {
		t.Fatalf("demo exit=%d", got)
	}
	// Corrupt a byte inside the video track's first sample payload. The
	// recomputed SHA-256 must disagree with the recorded one, which is a
	// verification failure (exit 2) rather than recoverable damage.
	path := filepath.Join(dir, "media", "cam_a_clean.mp4")
	sess, err := pipeline.OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	var target int64 = -1
	for _, tr := range sess.Movie.Tracks {
		if !tr.IsAudio() && len(tr.Samples) > 0 {
			target = tr.Samples[0].Offset
			break
		}
	}
	sess.Close()
	if target < 0 {
		t.Fatal("no video sample to corrupt")
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0xde, 0xad, 0xbe, 0xef}, target); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if got := runMain(t, "verify", "--dir", dir); got != 2 {
		t.Fatalf("tampered verify exit=%d want 2", got)
	}
}

func TestDemoFreshRemovesStaleOutput(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	stale := filepath.Join(dir, "stale.txt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := runMain(t, "demo", "--out", dir, "--fresh"); got != 0 {
		t.Fatalf("demo exit=%d", got)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("--fresh left stale files behind")
	}
}

func TestRepeatedVerifyIsByteIdentical(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	if got := runMain(t, "demo", "--out", dir); got != 0 {
		t.Fatalf("demo exit=%d", got)
	}
	m1, err := verify.Load(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := runMain(t, "verify", "--dir", dir); got != 0 {
		t.Fatalf("first verify exit=%d", got)
	}
	if got := runMain(t, "verify", "--dir", dir); got != 0 {
		t.Fatalf("second verify exit=%d", got)
	}
	m2, err := verify.Load(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m1.Summary.SegmentSHA256 != m2.Summary.SegmentSHA256 {
		t.Fatal("segment digest changed between verifies")
	}
	for i := range m1.Segments {
		if m1.Segments[i].SHA256 != m2.Segments[i].SHA256 {
			t.Fatalf("segment %s hash changed", m1.Segments[i].ID)
		}
	}
}

func TestVerifyRejectsMissingManifest(t *testing.T) {
	if got := runMain(t, "verify", "--dir", t.TempDir()); got != 2 {
		t.Fatalf("exit=%d want 2", got)
	}
}
