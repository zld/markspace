package job

import (
	"os"
	"path/filepath"
	"testing"

	"archive-recovery/internal/media"
)

func TestFailedValidationKeepsCheckpointOnDisk(t *testing.T) {
	dir := t.TempDir()
	cp := &Checkpoint{
		SchemaVersion: CheckpointSchemaVersion, InputSHA256: "aaa",
		ConfigDigest: "cfg", Codec: "av1", AdapterVersion: "av1-1",
		ChunkBytes: 16, TotalChunks: 2, Completed: []int{0},
		Status: StatusInterrupted,
	}
	if err := SaveCheckpoint(dir, cp); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Validate("bbb", "cfg", "av1-1"); media.ClassOf(err) != media.ClassCheckpoint {
		t.Fatalf("input mismatch must be a checkpoint error, got %v", err)
	}
	// The state file must survive the refusal so a compatible run can resume.
	if _, err := os.Stat(filepath.Join(dir, "checkpoint.json")); err != nil {
		t.Fatalf("checkpoint must be preserved after failed validation: %v", err)
	}
	// Adapter-version mismatch must also refuse rather than downgrade.
	if err := got.Validate("aaa", "cfg", "av1-2"); media.ClassOf(err) != media.ClassCheckpoint {
		t.Fatalf("adapter version mismatch must fail, got %v", err)
	}
}

func TestCompletedCheckpointNotResumable(t *testing.T) {
	cp := &Checkpoint{SchemaVersion: CheckpointSchemaVersion, Status: StatusComplete,
		InputSHA256: "a", ConfigDigest: "c", AdapterVersion: "av1-1"}
	if err := cp.Validate("a", "c", "av1-1"); media.ClassOf(err) != media.ClassCheckpoint {
		t.Fatalf("completed checkpoint must not resume, got %v", err)
	}
}

func TestIsCompletedMembership(t *testing.T) {
	cp := &Checkpoint{Completed: []int{0, 1, 3}}
	for _, i := range []int{0, 1, 3} {
		if !cp.IsCompleted(i) {
			t.Fatalf("chunk %d should be completed", i)
		}
	}
	for _, i := range []int{2, 4, 99} {
		if cp.IsCompleted(i) {
			t.Fatalf("chunk %d should not be completed", i)
		}
	}
}
