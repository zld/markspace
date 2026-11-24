package state

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"example.com/media-kernel/media"
)

func TestCheckpointSaveLoadAndSafeBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.json")
	store := &Store{Path: path}
	cp := NewCheckpoint("input-hash", "params-hash")
	if err := cp.RecordSegment(SegmentState{Index: 0, ID: "seg-0", SHA256: "h0", EndOffset: 128}); err != nil {
		t.Fatal(err)
	}
	if err := cp.RecordSegment(SegmentState{Index: 1, ID: "seg-1", SHA256: "h1", EndOffset: 256}); err != nil {
		t.Fatal(err)
	}
	if cp.SafeSegmentIndex != 2 {
		t.Fatalf("safe index=%d want 2", cp.SafeSegmentIndex)
	}
	if err := store.Save(cp); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.InputHash != "input-hash" || loaded.SafeSegmentIndex != 2 {
		t.Fatalf("loaded=%+v", loaded)
	}
	if !loaded.IsComplete(0, "h0") || loaded.IsComplete(0, "changed") {
		t.Fatal("IsComplete hash check failed")
	}
}

func TestCheckpointTamperDetected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.json")
	store := &Store{Path: path}
	if err := store.Save(NewCheckpoint("a", "b")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := []byte(`"input_hash": "z"`)
	b = append([]byte(nil), b...)
	old := []byte(`"input_hash": "a"`)
	if i := bytes.Index(b, old); i >= 0 {
		copy(b[i:], tampered)
	} else {
		t.Fatal("input hash field not found")
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !media.HasCode(err, media.CodeHashMismatch) {
		t.Fatalf("load err=%v want HASH_MISMATCH", err)
	}
}

func TestFailedValidationDoesNotOverwriteCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.json")
	store := &Store{Path: path}
	valid := NewCheckpoint("input-a", "params-a")
	if err := store.Save(valid); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	wrong := NewCheckpoint("input-b", "params-a")
	err = store.SaveChecked(wrong, "input-a", "params-a")
	if !media.HasCode(err, media.CodeHashMismatch) {
		t.Fatalf("SaveChecked err=%v want hash mismatch", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("valid checkpoint was overwritten after validation failure")
	}
}
