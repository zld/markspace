package checkpoint_test

import (
	"errors"
	"path/filepath"
	"testing"

	"mediacore/internal/checkpoint"
	"mediacore/internal/diagnostic"
)

func TestCheckpointSaveLoadAndInputMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "checkpoint.json")
	cp := checkpoint.New("input-a", "config-a")
	cp.Mark(checkpoint.EntryState{EntryNumber: 3, StableKey: "k3", IndexDigest: "d3"})
	if err := cp.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := checkpoint.Load(path, "input-a", "config-a")
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.IsDone("k3") || len(loaded.CompletedNumbers()) != 1 || loaded.CompletedNumbers()[0] != 3 {
		t.Fatalf("loaded checkpoint=%+v", loaded)
	}
	_, err = checkpoint.Load(path, "input-b", "config-a")
	if !errors.Is(err, diagnostic.ErrCheckpoint) || diagnostic.ExitCode(err) != 3 {
		t.Fatalf("mismatch err=%v code=%d", err, diagnostic.ExitCode(err))
	}
}

func TestMissingCheckpointIsEmpty(t *testing.T) {
	cp, err := checkpoint.Load(filepath.Join(t.TempDir(), "missing.json"), "i", "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Entries) != 0 || cp.CheckpointID == "" {
		t.Fatalf("unexpected checkpoint=%+v", cp)
	}
}
