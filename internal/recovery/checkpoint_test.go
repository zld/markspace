package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/fault"
)

func TestCheckpointResumesEachLayerAtNextBoundary(t *testing.T) {
	dir := t.TempDir()
	layers := []string{"1080p", "720p", "480p"}
	cp := NewCheckpoint(dir, "input-sha", "params-sha", "ios", layers)
	if err := cp.CommitSegment("1080p", 0, "h0"); err != nil {
		t.Fatal(err)
	}
	if err := cp.CommitSegment("1080p", 1, "h1"); err != nil {
		t.Fatal(err)
	}
	if err := cp.CommitSegment("720p", 0, "g0"); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadOrNew(dir, "input-sha", "params-sha", "ios", layers)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.NextIndex("1080p"); got != 2 {
		t.Fatalf("1080p next=%d want 2", got)
	}
	if got := loaded.NextIndex("720p"); got != 1 {
		t.Fatalf("720p next=%d want 1", got)
	}
	if got := loaded.NextIndex("480p"); got != 0 {
		t.Fatalf("480p next=%d want 0", got)
	}
	if loaded.Layers["1080p"].SegmentHashes[1] != "h1" {
		t.Fatal("segment hash not persisted")
	}
}

func TestDigestMismatchRejectsResume(t *testing.T) {
	dir := t.TempDir()
	cp := NewCheckpoint(dir, "old-input", "old-params", "ios", []string{"720p"})
	if err := cp.CommitSegment("720p", 0, "h"); err != nil {
		t.Fatal(err)
	}
	_, err := LoadOrNew(dir, "new-input", "old-params", "ios", []string{"720p"})
	if fault.Classify(err) != fault.DigestMismatch {
		t.Fatalf("kind=%q err=%v", fault.Classify(err), err)
	}
}

func TestPublishAndRollbackRetainsPrevious(t *testing.T) {
	dir := t.TempDir()
	if _, err := Publish(dir, []byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(dir, []byte(`{"version":2}`)); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(filepath.Join(dir, "published", "current.json"))
	if err != nil || string(current) != `{"version":2}` {
		t.Fatalf("current=%s err=%v", current, err)
	}
	rolled, err := Rollback(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(rolled) != `{"version":1}` {
		t.Fatalf("rolled=%s", rolled)
	}
	current, _ = os.ReadFile(filepath.Join(dir, "published", "current.json"))
	if string(current) != `{"version":1}` {
		t.Fatalf("current after rollback=%s", current)
	}
	if _, err := os.Stat(filepath.Join(dir, "published", "previous.json")); err != nil {
		t.Fatalf("previous not retained: %v", err)
	}
}

func TestMissingCheckpointIsNotNotExistWrappedWrongly(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}
