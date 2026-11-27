package batch

import (
	"os"
	"path/filepath"
	"testing"

	merr "mediatool/internal/errors"
	"mediatool/internal/index"
)

func writeChunk(t *testing.T, dir, name string, body []byte) (string, string) {
	t.Helper()
	rel := filepath.Join("chunks", name)
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return rel, index.HashBytes(body)
}

func TestCommitSkipsAndResume(t *testing.T) {
	dir := t.TempDir()
	cp, err := New(dir, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := cp.PendingChunks(); len(got) != 3 || got[0] != 0 {
		t.Fatalf("initial pending = %v", got)
	}

	rel, sha := writeChunk(t, dir, "0.pcm", []byte("chunk-zero"))
	if err := cp.Commit(index.Record{
		ChunkID: 0, InputDigest: "in0", OutputPath: rel,
		StartNanos: 0, EndNanos: 1e9, SampleCount: 48,
	}, sha); err != nil {
		t.Fatal(err)
	}
	if cp.CommittedCount() != 1 {
		t.Fatal("commit not recorded")
	}

	// Reopen: committed chunk must be verified and skipped.
	cp2, err := New(dir, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := cp2.Verify(); err != nil {
		t.Fatalf("resume verify failed: %v", err)
	}
	if got := cp2.PendingChunks(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("resume pending = %v, want [1 2]", got)
	}
}

func TestCommitRejectsPartialOutput(t *testing.T) {
	dir := t.TempDir()
	cp, _ := New(dir, 1)
	rel, _ := writeChunk(t, dir, "0.pcm", []byte("actual"))
	// Record a digest that does not match the file on disk.
	err := cp.Commit(index.Record{ChunkID: 0, OutputPath: rel}, index.HashBytes([]byte("different")))
	if err == nil || merr.AsCode(err) != merr.CodeChunkIncomplete {
		t.Fatalf("partial/mismatched output accepted: %v", err)
	}
	if cp.CommittedCount() != 0 {
		t.Fatal("checkpoint must not point at uncommitted data")
	}
}

func TestVerifyReportsFirstMismatch(t *testing.T) {
	dir := t.TempDir()
	cp, _ := New(dir, 2)
	rel0, sha0 := writeChunk(t, dir, "0.pcm", []byte("c0"))
	rel1, sha1 := writeChunk(t, dir, "1.pcm", []byte("c1"))
	if err := cp.Commit(index.Record{ChunkID: 0, OutputPath: rel0}, sha0); err != nil {
		t.Fatal(err)
	}
	if err := cp.Commit(index.Record{ChunkID: 1, OutputPath: rel1}, sha1); err != nil {
		t.Fatal(err)
	}
	// Corrupt chunk 0's output after the fact.
	if err := os.WriteFile(filepath.Join(dir, rel0), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	cp2, _ := New(dir, 2)
	err := cp2.Verify()
	var me *merr.MediaError
	if !merr.As(err, &me) {
		t.Fatalf("expected MediaError, got %v", err)
	}
	if me.Offset != 0 {
		t.Fatalf("first inconsistent chunk = %d, want 0", me.Offset)
	}
	if merr.AsCode(err) != merr.CodeChunkIncomplete {
		t.Fatalf("code = %s", merr.AsCode(err))
	}
	if merr.AsCode(err).ExitCode() != 3 {
		t.Fatalf("mismatch must map to exit 3, got %d", merr.AsCode(err).ExitCode())
	}
}

func TestCheckpointCorruptRejected(t *testing.T) {
	dir := t.TempDir()
	cp, _ := New(dir, 1)
	rel, sha := writeChunk(t, dir, "0.pcm", []byte("c0"))
	if err := cp.Commit(index.Record{ChunkID: 0, OutputPath: rel}, sha); err != nil {
		t.Fatal(err)
	}
	// Truncate the checkpoint to invalid JSON.
	if err := os.WriteFile(filepath.Join(dir, "checkpoint.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir, 1); err == nil ||
		merr.AsCode(err) != merr.CodeCheckpointCorrupt {
		t.Fatalf("corrupt checkpoint accepted: %v", err)
	}
}

func TestMarkFailedDoesNotCommit(t *testing.T) {
	dir := t.TempDir()
	cp, _ := New(dir, 2)
	if err := cp.MarkFailed(0); err != nil {
		t.Fatal(err)
	}
	if cp.CommittedCount() != 0 {
		t.Fatal("failed chunk counted as committed")
	}
	cp2, _ := New(dir, 2)
	if got := cp2.PendingChunks(); len(got) != 2 {
		t.Fatalf("failed chunk should remain redoable, pending=%v", got)
	}
}
