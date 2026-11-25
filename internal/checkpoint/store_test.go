package checkpoint

import (
	"path/filepath"
	"testing"

	"mediascan/internal/errs"
	"mediascan/internal/fingerprint"
)

func testIdentity() Identity {
	return Identity{
		InputHash:    "input-hash-1",
		ManifestHash: "manifest-hash-1",
		RuleVersion:  fingerprint.RuleVersion,
	}
}

func TestCommitAndReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := testIdentity()
	if err := s.Commit(Chunk{
		ID: 0, InputHash: id.InputHash, ManifestHash: id.ManifestHash,
		RuleVersion: id.RuleVersion, State: StateCommitted,
		SegmentHashes: []string{"h0", "h1"},
	}); err != nil {
		t.Fatal(err)
	}
	// Failed chunk is stored but never reusable.
	if err := s.Commit(Chunk{
		ID: 1, InputHash: id.InputHash, ManifestHash: id.ManifestHash,
		RuleVersion: id.RuleVersion, State: StateFailed, ErrorCount: 2,
	}); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Reusable(0, id); got == nil || got.State != StateCommitted {
		t.Fatalf("committed chunk 0 should be reusable, got %+v", got)
	}
	if reopened.Reusable(1, id) != nil {
		t.Fatal("failed chunk must not be reusable")
	}
	if reopened.CommittedCount() != 1 || reopened.ChunkCount() != 2 {
		t.Fatalf("counts = %d/%d", reopened.CommittedCount(), reopened.ChunkCount())
	}
}

func TestIdentityMismatchForcesRedo(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	id := testIdentity()
	_ = s.Commit(Chunk{
		ID: 0, InputHash: id.InputHash, ManifestHash: id.ManifestHash,
		RuleVersion: id.RuleVersion, State: StateCommitted,
	})
	// Manifest changed: same input hash but different manifest hash.
	changed := id
	changed.ManifestHash = "manifest-hash-2"
	if s.Reusable(0, changed) != nil {
		t.Fatal("manifest hash change must invalidate the checkpoint")
	}
	// Stale rule version likewise.
	stale := id
	stale.RuleVersion = "rules-v0"
	if s.Reusable(0, stale) != nil {
		t.Fatal("rule version change must invalidate the checkpoint")
	}
}

func TestFirstIncomplete(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	id := testIdentity()
	for _, cid := range []int{0, 1} {
		_ = s.Commit(Chunk{
			ID: cid, InputHash: id.InputHash, ManifestHash: id.ManifestHash,
			RuleVersion: id.RuleVersion, State: StateCommitted,
		})
	}
	if got := s.FirstIncomplete(4, id); got != 2 {
		t.Fatalf("first incomplete = %d, want 2", got)
	}
	// A failed chunk counts as incomplete, so resume restarts there.
	_ = s.Commit(Chunk{
		ID: 2, InputHash: id.InputHash, ManifestHash: id.ManifestHash,
		RuleVersion: id.RuleVersion, State: StateFailed,
	})
	if got := s.FirstIncomplete(4, id); got != 2 {
		t.Fatalf("failed chunk must be first incomplete, got %d", got)
	}
	if got := s.FirstIncomplete(2, id); got != -1 {
		t.Fatalf("all complete = %d, want -1", got)
	}
}

func TestAtomicIndexWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	snap := IndexSnapshot{
		RuleVersion: fingerprint.RuleVersion,
		Records: []fingerprint.Record{
			{Hash: "abc", ByteLength: 10, Refs: []fingerprint.SourceRef{{Source: "x"}}},
		},
		Keyframes: []KeyframeDTO{
			{TrackID: 1, SampleNumber: 1, SegmentIndex: 0, State: "known"},
		},
	}
	if err := s.WriteIndex(snap); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if got.RuleVersion != fingerprint.RuleVersion || len(got.Records) != 1 ||
		got.Records[0].Hash != "abc" || len(got.Keyframes) != 1 {
		t.Fatalf("index round trip lost data: %+v", got)
	}
	// No leftover temp file after the atomic rename.
	if _, err := readDirTemp(dir); err == nil {
		t.Fatal("temporary index file should be removed after rename")
	}
}

func TestReadIndexMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if _, err := s.ReadIndex(); err == nil {
		t.Fatal("missing index must error")
	}
	// Write a structurally invalid JSON index.
	path := filepath.Join(dir, dirName, indexName)
	_ = writeFile(path, []byte("{not json"))
	_, err := s.ReadIndex()
	se, _ := errs.AsError(err)
	if se == nil || se.Kind != errs.KindCheckpoint {
		t.Fatalf("want checkpoint error for corrupt index, got %v", err)
	}
}

func TestCorruptStoreRejected(t *testing.T) {
	dir := t.TempDir()
	_ = mkdirAll(filepath.Join(dir, dirName))
	_ = writeFile(filepath.Join(dir, dirName, storeName), []byte("{bad"))
	if _, err := Open(dir); err == nil {
		t.Fatal("corrupt store must not open silently")
	}
}

func TestCommitReplacesSameID(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	id := testIdentity()
	_ = s.Commit(Chunk{ID: 0, RuleVersion: id.RuleVersion, State: StateFailed})
	// Redo the interrupted chunk: the failed entry is replaced, not duplicated.
	_ = s.Commit(Chunk{
		ID: 0, InputHash: id.InputHash, ManifestHash: id.ManifestHash,
		RuleVersion: id.RuleVersion, State: StateCommitted,
	})
	if s.ChunkCount() != 1 || s.CommittedCount() != 1 {
		t.Fatalf("replacement failed: count=%d committed=%d", s.ChunkCount(), s.CommittedCount())
	}
}
