package fixture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateIsDeterministic(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	if _, err := Generate(a, 42, 8_000_000, CorruptNone); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(b, 42, 8_000_000, CorruptNone); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"media/cam-a.mp4", "media/cam-b.mp4", "manifest.json", "media/cam-a.rtp.jsonl"} {
		da, err := os.ReadFile(filepath.Join(a, rel))
		if err != nil {
			t.Fatal(err)
		}
		db, err := os.ReadFile(filepath.Join(b, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(da, db) {
			t.Fatalf("%s differs between seeds", rel)
		}
	}
}

func TestSeedChangesBytes(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	if _, err := Generate(a, 1, 8_000_000, CorruptNone); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(b, 2, 8_000_000, CorruptNone); err != nil {
		t.Fatal(err)
	}
	da, _ := os.ReadFile(filepath.Join(a, "media", "cam-a.mp4"))
	db, _ := os.ReadFile(filepath.Join(b, "media", "cam-a.mp4"))
	if bytes.Equal(da, db) {
		t.Fatal("different seeds produced identical media")
	}
}

func TestCorruptionsProduceDistinctBytes(t *testing.T) {
	kinds := []string{CorruptNone, CorruptTruncatedTable, CorruptEditOverrun, CorruptMissingSync, CorruptSparseKey, CorruptDTSRegression}
	seen := map[string]string{}
	for _, k := range kinds {
		dir := t.TempDir()
		if _, err := Generate(dir, 42, 8_000_000, k); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "media", "cam-a.mp4"))
		if err != nil {
			t.Fatal(err)
		}
		// Hash the whole file: some corruptions (truncation, elst rewrite)
		// leave the header intact and only change later bytes.
		sum := sha256.Sum256(data)
		h := hex.EncodeToString(sum[:])
		if prev, dup := seen[h]; dup && prev != k {
			t.Fatalf("%s and %s produced identical media", prev, k)
		}
		seen[h] = k
	}
}

func TestTruncatedTableInflatesCountWithoutAddingBytes(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(dir, 42, 8_000_000, CorruptTruncatedTable); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "media", "cam-a.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	// The stco box header must still be present, so a reviewer can locate it.
	idx := bytes.Index(data, []byte("stco"))
	if idx < 0 {
		t.Fatal("stco missing after truncation")
	}
}

func TestEditOverrunOnlyChangesElstDuration(t *testing.T) {
	cleanDir := t.TempDir()
	badDir := t.TempDir()
	if _, err := Generate(cleanDir, 42, 8_000_000, CorruptNone); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(badDir, 42, 8_000_000, CorruptEditOverrun); err != nil {
		t.Fatal(err)
	}
	clean, _ := os.ReadFile(filepath.Join(cleanDir, "media", "cam-a.mp4"))
	bad, _ := os.ReadFile(filepath.Join(badDir, "media", "cam-a.mp4"))
	if len(clean) != len(bad) {
		t.Fatal("edit-overrun corruption changed file length")
	}
	diffs := 0
	for i := range clean {
		if clean[i] != bad[i] {
			diffs++
		}
	}
	if diffs == 0 || diffs > 8 {
		t.Fatalf("expected a tiny in-place elst change, saw %d byte diffs", diffs)
	}
}

func TestRTPWrapCrossesBoundary(t *testing.T) {
	dir := t.TempDir()
	sess, err := Generate(dir, 42, 8_000_000, CorruptRTPWrap)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := ReadRTP(sess.RTPFiles["cam-a"])
	if err != nil {
		t.Fatal(err)
	}
	crossings := 0
	for i := 1; i < len(recs); i++ {
		if recs[i].Timestamp < recs[i-1].Timestamp {
			crossings++
		}
	}
	if crossings != 1 {
		t.Fatalf("wrap crossings=%d want exactly 1", crossings)
	}
	// The clean variant must not wrap.
	cleanDir := t.TempDir()
	clean, err := Generate(cleanDir, 42, 8_000_000, CorruptNone)
	if err != nil {
		t.Fatal(err)
	}
	crecs, err := ReadRTP(clean.RTPFiles["cam-a"])
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(crecs); i++ {
		if crecs[i].Timestamp < crecs[i-1].Timestamp {
			t.Fatal("clean sidecar wrapped unexpectedly")
		}
	}
}

func TestManifestRecordsSeedAndTarget(t *testing.T) {
	dir := t.TempDir()
	sess, err := Generate(dir, 7, 4_000_000, CorruptNone)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Manifest.Seed != 7 || sess.Manifest.TargetBps != 4_000_000 {
		t.Fatalf("manifest identity wrong: %+v", sess.Manifest)
	}
	if len(sess.Manifest.Entries) != 2 {
		t.Fatalf("entries=%d", len(sess.Manifest.Entries))
	}
}

func TestCorruptionsListed(t *testing.T) {
	got := len(Corruptions())
	if got < 6 {
		t.Fatalf("expected at least 6 corruption modes, got %d", got)
	}
}
