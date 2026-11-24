package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIndexCorruptionRebuildsValidChunkPrefix(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	idx := &Index{InputSHA256: "in", ParamsSHA256: "p", ChunkCount: 4, ChunkSize: 16}
	for i := 0; i < 3; i++ {
		if err := s.CommitChunk(idx, Chunk{Index: i}, []byte{byte('a' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	if idx.NextChunk != 3 {
		t.Fatalf("next=%d want 3", idx.NextChunk)
	}
	if err := os.WriteFile(s.IndexPath, []byte("{broken json"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, rec, err := s.LoadOrRecover("in", "p", 4, 16)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.IndexCorrupt || loaded.NextChunk != 3 || rec.ResumedFromChunk != 3 {
		t.Fatalf("recovery=%+v next=%d", rec, loaded.NextChunk)
	}
}

func TestCorruptChunkRollsBackToNearestValidPrefix(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	idx := &Index{InputSHA256: "in", ChunkCount: 4, ChunkSize: 16}
	for i := 0; i < 4; i++ {
		if err := s.CommitChunk(idx, Chunk{Index: i}, []byte("chunk-"+string(rune('0'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(s.ChunkPath(2), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, rec, err := s.LoadOrRecover("in", "", 4, 16)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.NextChunk != 2 || rec.ResumedFromChunk != 2 {
		t.Fatalf("next=%d rec=%+v want rollback to chunk 2", loaded.NextChunk, rec)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.json")); err != nil {
		t.Fatalf("repaired index not saved: %v", err)
	}
}

func TestInputDigestMismatchDoesNotReuseChunks(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	idx := &Index{InputSHA256: "old", ChunkCount: 2}
	if err := s.CommitChunk(idx, Chunk{Index: 0}, []byte("x")); err != nil {
		t.Fatal(err)
	}
	loaded, rec, err := s.LoadOrRecover("new", "", 2, 16)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.InputMismatch || loaded.NextChunk != 0 {
		t.Fatalf("loaded=%+v rec=%+v", loaded, rec)
	}
}
