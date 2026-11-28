package index

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashMetaDeterministic(t *testing.T) {
	a := HashMeta(map[string]string{"b": "2", "a": "1"})
	b := HashMeta(map[string]string{"a": "1", "b": "2"})
	if a != b {
		t.Fatal("map order leaked into digest")
	}
	if a == HashMeta(map[string]string{"a": "1", "b": "3"}) {
		t.Fatal("value change did not change digest")
	}
}

func TestHashFileTracksContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.bin")
	if err := os.WriteFile(p, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	h1, err := HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	h2, err := HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("content change did not change digest")
	}
}

func TestStoreCommitAndReuse(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.CommitCount() != 0 {
		t.Fatal("fresh store has commits")
	}
	rec := AssetRecord{ID: "video:ios", Platform: "ios", Digest: "abc", Output: "o"}
	s.Stage(rec)
	if _, ok := s.Lookup(rec.ID, rec.Digest); ok {
		t.Fatal("staged record visible before commit")
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if s.CommitCount() != 1 {
		t.Fatalf("commit count = %d", s.CommitCount())
	}
	// Reopen: committed state survives; digest mismatch forces rebuild.
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Lookup(rec.ID, rec.Digest); !ok {
		t.Fatal("committed record not reusable")
	}
	if _, ok := s2.Lookup(rec.ID, "different"); ok {
		t.Fatal("digest mismatch still reused")
	}
}

func TestCheckpointResume(t *testing.T) {
	dir := t.TempDir()
	b0 := []AssetRecord{{ID: "a", Digest: "da"}, {ID: "b", Digest: "db"}}
	b1 := []AssetRecord{{ID: "c", Digest: "dc"}, {ID: "d", Digest: "dd"}}
	if err := AppendBlock(dir, 0, b0); err != nil {
		t.Fatal(err)
	}
	if err := AppendBlock(dir, 1, b1); err != nil {
		t.Fatal(err)
	}
	recs, next, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if next != 2 || len(recs) != 4 {
		t.Fatalf("next=%d recs=%d", next, len(recs))
	}

	// Corrupt block 1: resume must fall back to the end of block 0.
	if err := os.WriteFile(blockPath(dir, 1), []byte(`{"records":[],"sha256":"bad"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	recs, next, err = LoadCheckpoint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if next != 1 || len(recs) != 2 {
		t.Fatalf("after corruption: next=%d recs=%d", next, len(recs))
	}

	// No checkpoint at all: start from block 0.
	recs, next, err = LoadCheckpoint(t.TempDir())
	if err != nil || next != 0 || len(recs) != 0 {
		t.Fatalf("empty: next=%d recs=%d err=%v", next, len(recs), err)
	}
}

func TestCheckpointDataBeforeCommit(t *testing.T) {
	dir := t.TempDir()
	// Simulate a crash: data block written, checkpoint never committed.
	if err := atomicWriteJSON(filepath.Join(dir, "blocks"), "block-0000.json",
		blockFile{Records: []AssetRecord{{ID: "a"}}, SHA256: "x"}); err != nil {
		t.Fatal(err)
	}
	recs, next, err := LoadCheckpoint(dir)
	if err != nil || next != 0 || len(recs) != 0 {
		t.Fatalf("orphan block must not be adopted: next=%d recs=%d err=%v",
			next, len(recs), err)
	}
}
