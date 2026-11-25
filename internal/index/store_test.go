package index

import (
	"os"
	"path/filepath"
	"testing"
)

func rec(i int, hash string) BlockRecord {
	return BlockRecord{
		Index: i, StartSample: int64(i) * 16, EndSample: int64(i+1) * 16,
		ContentHash: hash, Complete: true, PeakDBFS: -12.5,
	}
}

func TestStoreAtomicCommitAndReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.CommitBlock(rec(i, "hash-"+string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	if s.BlockCount() != 3 {
		t.Fatalf("count=%d", s.BlockCount())
	}
	// No temp file left after atomic rename.
	if _, err := os.Stat(filepath.Join(dir, "index.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("temp record left behind")
	}
	// Reopen: identical verified state.
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s2.BlockCount() != 3 {
		t.Fatalf("reopened count=%d", s2.BlockCount())
	}
	if h, ok := s2.FindHash(1); !ok || h != "hash-b" {
		t.Fatalf("stored hash wrong: %q %v", h, ok)
	}
}

func TestStoreCorruptCheckpointRollsBack(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	for i := 0; i < 4; i++ {
		if err := s.CommitBlock(rec(i, "h")); err != nil {
			t.Fatal(err)
		}
	}
	// Corrupt the file with invalid JSON: recovery must start from zero
	// valid blocks rather than failing the run.
	p := filepath.Join(dir, "index.json")
	if err := os.WriteFile(p, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("corrupt index should recover, got %v", err)
	}
	rb, ok := s2.Rollback()
	if !ok {
		t.Fatal("expected rollback report")
	}
	if s2.BlockCount() != 0 {
		t.Fatalf("want zero recoverable blocks, got %d", s2.BlockCount())
	}
	_ = rb
	// New commits append after the valid boundary and persist.
	if err := s2.CommitBlock(rec(0, "new")); err != nil {
		t.Fatal(err)
	}
}

func TestStoreIncompleteRecordRollsBackToBoundary(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	s.CommitBlock(rec(0, "a"))
	s.CommitBlock(rec(1, "b"))
	// Manually inject an incomplete tail record.
	s.state.Blocks = append(s.state.Blocks, BlockRecord{Index: 2, Complete: false})
	s.persist()
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s2.BlockCount() != 2 {
		t.Fatalf("want 2 valid blocks, got %d", s2.BlockCount())
	}
	rb, _ := s2.Rollback()
	if rb.Dropped != 1 {
		t.Fatalf("dropped=%d want 1", rb.Dropped)
	}
}

func TestStoreTruncateInvalidatesDependents(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	for i := 0; i < 5; i++ {
		s.CommitBlock(rec(i, "h"))
	}
	dropped, err := s.TruncateFrom(2)
	if err != nil || dropped != 3 {
		t.Fatalf("dropped=%d err=%v", dropped, err)
	}
	if s.BlockCount() != 2 {
		t.Fatalf("count after truncate=%d", s.BlockCount())
	}
	if _, ok := s.FindHash(3); ok {
		t.Fatal("truncated hash still readable")
	}
}

func TestStoreContainerSeparate(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	s.CommitBlock(rec(0, "pcm-hash"))
	if err := s.SetContainer(ContainerRecord{Hash: "box-hash", Timescale: 48000}); err != nil {
		t.Fatal(err)
	}
	s2, _ := OpenStore(dir)
	c, ok := s2.Container()
	if !ok || c.Hash != "box-hash" || c.Timescale != 48000 {
		t.Fatalf("container record lost: %+v %v", c, ok)
	}
}
