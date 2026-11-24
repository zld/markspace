package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func sampleCheckpoint(key string) Checkpoint {
	return Checkpoint{
		Key:          key,
		Dependencies: []string{"ingest", "analyze"},
		OutputHash:   "out-hash-1",
		SourceOffsets: []SourceOffset{
			{Input: "sample.ts", Start: 0, End: 1880},
		},
		NodeVersion: CodeVersion,
	}
}

func TestCheckpointSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cp := sampleCheckpoint("key-1")
	if err := store.Save(cp); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Load("key-1")
	if err != nil || !ok {
		t.Fatalf("load = ok %v err %v", ok, err)
	}
	if got.OutputHash != "out-hash-1" || len(got.Dependencies) != 2 {
		t.Fatalf("round trip lost data: %+v", got)
	}
	if got.SourceOffsets[0].End != 1880 {
		t.Fatalf("source offsets not persisted: %+v", got.SourceOffsets)
	}
	keys, _ := store.Keys()
	if len(keys) != 1 || keys[0] != "key-1" {
		t.Fatalf("keys = %v", keys)
	}
}

func TestCheckpointMissingAndRemove(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	if _, ok, err := store.Load("absent"); err != nil || ok {
		t.Fatalf("missing key: ok=%v err=%v", ok, err)
	}
	if err := store.Remove("absent"); err != nil {
		t.Fatalf("removing an absent checkpoint must be a no-op, got %v", err)
	}
	if err := store.Save(sampleCheckpoint("k")); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove("k"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Load("k"); ok {
		t.Fatal("checkpoint must be removed")
	}
}

func TestCheckpointSaveRejectsEmptyKey(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	if err := store.Save(Checkpoint{}); err == nil {
		t.Fatal("empty key must be rejected")
	}
}

func TestCheckpointAtomicWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)
	for i := 0; i < 5; i++ {
		if err := store.Save(sampleCheckpoint("atomic")); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if len(e.Name()) > 4 && e.Name()[:4] == ".tmp" {
			t.Fatalf("temporary file leaked: %s", e.Name())
		}
	}
	if _, ok, _ := store.Load("atomic"); !ok {
		t.Fatal("final checkpoint must exist after atomic replace")
	}
}

func TestCheckpointValidityAgainstNode(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	node := &Node{Name: "fp", Inputs: []string{"in-1"}, Params: map[string]int{"w": 2}}
	key, _ := node.Key()
	cp := sampleCheckpoint(key)
	if err := store.Save(cp); err != nil {
		t.Fatal(err)
	}
	loaded, ok, _ := store.Load(key)
	if !ok || !store.Valid(loaded, node) {
		t.Fatal("matching inputs/params/version must be valid")
	}
	// Changed input changes the node key and invalidates the checkpoint.
	changed := &Node{Name: "fp", Inputs: []string{"in-2"}, Params: map[string]int{"w": 2}}
	if store.Valid(loaded, changed) {
		t.Fatal("changed input must invalidate the checkpoint")
	}
	// A stale code version invalidates even a key match.
	stale := loaded
	stale.NodeVersion = "mediad-0"
	if store.Valid(stale, node) {
		t.Fatal("older code version must invalidate the checkpoint")
	}
}

func TestNewStoreCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "cp")
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(sampleCheckpoint("x")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("checkpoint directory not created: %v", err)
	}
}
