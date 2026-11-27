package index

import (
	"os"
	"path/filepath"
	"testing"

	"mediabatch/internal/clock"
)

func sampleStore() *Store {
	s := &Store{Version: currentVersion, Artifacts: "artifacts"}
	s.Put(Entry{Key: "k1", ContentSHA: "aa", Size: 4,
		Start: clock.Zero, End: clock.MustNew(4, 1), Artifact: "artifacts/aa.mp4", Chunk: 0})
	s.Put(Entry{Key: "k2", ContentSHA: "bb", Size: 5,
		Start: clock.MustNew(4, 1), End: clock.MustNew(8, 1), Artifact: "artifacts/bb.mp4", Chunk: 0})
	s.CommitChunk(0, "digest0", []string{"k1", "k2"})
	return s
}

func TestSaveOpenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := sampleStore().Save(dir); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 2 {
		t.Fatalf("entries=%d", len(s.Entries))
	}
	if _, ok := s.Lookup("k2"); !ok {
		t.Fatal("k2 missing")
	}
	cs, ok := s.Chunk(0)
	if !ok || !cs.Committed || cs.InputDigest != "digest0" {
		t.Fatalf("chunk=%+v", cs)
	}
	if s.MaxCommittedChunk() != 1 {
		t.Fatalf("max=%d", s.MaxCommittedChunk())
	}
}

func TestOpenMissingCreatesEmpty(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 0 || s.Artifacts != "artifacts" {
		t.Fatalf("store=%+v", s)
	}
}

func TestCorruptIndex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	os.WriteFile(path, []byte("{not json"), 0o644)
	if _, err := Open(dir); err == nil {
		t.Fatal("expected corruption error")
	}
	os.WriteFile(path, []byte(`{"version":1,"entries":[{"key":"","artifact":""}]}`), 0o644)
	if _, err := Open(dir); err == nil {
		t.Fatal("expected validation error")
	}
	os.WriteFile(path, []byte(`{"version":99}`), 0o644)
	if _, err := Open(dir); err == nil {
		t.Fatal("expected version error")
	}
}

func TestRemoveEntriesExcept(t *testing.T) {
	s := sampleStore()
	s.CommitChunk(1, "d1", []string{"k3"})
	s.Put(Entry{Key: "k3", Artifact: "artifacts/cc.mp4", Chunk: 1})
	removed := s.RemoveEntriesExcept([]string{"k1", "k2"}, []string{"k1"})
	if removed != 1 {
		t.Fatalf("removed=%d", removed)
	}
	if _, ok := s.Lookup("k2"); ok {
		t.Fatal("k2 should be gone")
	}
	if _, ok := s.Lookup("k3"); !ok {
		t.Fatal("k3 belongs to another chunk and must stay")
	}
}

func TestAtomicWriteLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	if err := sampleStore().Save(dir); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}
