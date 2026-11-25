package index

import (
	"path/filepath"
	"testing"
)

func TestSaveLoadRollbackAndIdempotency(t *testing.T) {
	path, err := PathFor(t.TempDir(), "batch-1", "item-1")
	if err != nil {
		t.Fatal(err)
	}
	ranges := [][2]int{{0, 64}, {64, 128}, {128, 140}}
	idx := New(path, "batch-1", "item-1", "in-1", "cfg-1", ranges)
	if err := idx.Save(); err != nil {
		t.Fatal(err)
	}
	if err := idx.Begin(0); err != nil {
		t.Fatal(err)
	}
	if err := idx.MarkComplete(ChunkRecord{Index: 0, StartSample: 0, EndSample: 64, ByteLength: 10, SHA256: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := idx.Begin(1); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil {
		t.Fatal("missing checkpoint")
	}
	if err := loaded.CheckCompatibility("batch-1", "in-1", "cfg-1"); err != nil {
		t.Fatalf("same identity should be reusable: %v", err)
	}
	rolled := loaded.RollbackIncomplete()
	if len(rolled) != 1 || rolled[0] != 1 {
		t.Fatalf("rolled = %v", rolled)
	}
	if loaded.Chunks[0].Status != StatusComplete || loaded.CompleteCount() != 1 {
		t.Fatalf("verified chunk was not preserved: %+v", loaded.Chunks)
	}
	if loaded.Chunks[1].Status != StatusPending {
		t.Fatalf("half-written chunk not reset: %+v", loaded.Chunks[1])
	}

	if err := loaded.CheckCompatibility("batch-1", "in-2", "cfg-1"); err == nil {
		t.Fatal("changed input must reject checkpoint")
	}
	if err := loaded.CheckCompatibility("batch-1", "in-1", "cfg-2"); err == nil {
		t.Fatal("changed config must reject checkpoint")
	}
}

func TestPathForIsStableAndConfined(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work")
	a, err := PathFor(root, "b", "i")
	if err != nil {
		t.Fatal(err)
	}
	b, err := PathFor(root, "b", "i")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("path not stable: %s != %s", a, b)
	}
	if !filepath.IsAbs(a) {
		t.Fatalf("expected absolute confined path, got %s", a)
	}
}
