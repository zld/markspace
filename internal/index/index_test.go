package index

import (
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/mederr"
)

func sampleRecord(i int) Record {
	return Record{
		Representation: "v-hi",
		Track:          "video",
		ObservedNS:     int64(i) * 40_000_000,
		CorrectedNS:    int64(i) * 33_366_667,
		SegmentNumber:  i/4 + 1,
		ByteOffset:     int64(i * 128),
		InputSize:      1 << 20,
	}
}

func TestAddLookupAndBoundary(t *testing.T) {
	ix, err := New(4, MaxBatchRecords)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := ix.Add(sampleRecord(i)); err != nil {
			t.Fatal(err)
		}
	}
	// Nearest keyframe not later than the target.
	got, ok := ix.Lookup(33_366_667 * 3)
	if !ok || got.CorrectedNS != 33_366_667*3 {
		t.Fatalf("exact lookup failed: %+v", got)
	}
	got, ok = ix.Lookup(33_366_667*3 + 10)
	if !ok || got.CorrectedNS != 33_366_667*3 {
		t.Fatal("floor lookup must return the preceding keyframe")
	}
	if _, ok := ix.Lookup(-1); ok {
		t.Fatal("lookup before first keyframe must return false")
	}
	// Offset outside input size is rejected (cross-stage invariant).
	bad := sampleRecord(0)
	bad.ByteOffset = bad.InputSize
	if err := ix.Add(bad); !mederr.HasCode(err, mederr.CodeInvalidManifest) {
		t.Fatalf("out-of-range offset must fail, got %v", err)
	}
}

func TestChunkCommitAndPending(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCheckpointStore(dir, filepath.Join(dir, "ckpt"))
	if err != nil {
		t.Fatal(err)
	}
	ix, _ := New(4, MaxBatchRecords)
	for i := 0; i < 10; i++ {
		_ = ix.Add(sampleRecord(i))
	}
	pending := ix.PendingChunks()
	if len(pending) != 2 || len(pending[0]) != 4 {
		t.Fatalf("pending chunks wrong: %d", len(pending))
	}
	for seq, chunk := range pending {
		if err := store.Commit(ix, seq, chunk); err != nil {
			t.Fatal(err)
		}
	}
	if ix.CommittedChunks() != 2 || len(ix.Tail()) != 2 {
		t.Fatalf("commit bookkeeping wrong: %d tail=%d",
			ix.CommittedChunks(), len(ix.Tail()))
	}
	// Out-of-order commit must be rejected.
	if err := ix.MarkChunkCommitted(9); !mederr.HasCode(
		err, mederr.CodeCheckpointCorrupt) {
		t.Fatalf("out-of-order commit must fail, got %v", err)
	}
}

func TestTruncatedIndexRecovery(t *testing.T) {
	dir := t.TempDir()
	ckpt := filepath.Join(dir, "ckpt")
	store, _ := NewCheckpointStore(dir, ckpt)
	ix, _ := New(4, MaxBatchRecords)
	for i := 0; i < 10; i++ {
		_ = ix.Add(sampleRecord(i))
	}
	for seq, chunk := range ix.PendingChunks() {
		if err := store.Commit(ix, seq, chunk); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a crash: corrupt the second chunk's payload and leave a
	// stray higher-numbered partial file.
	target := chunkPath(ckpt, 1)
	data, _ := os.ReadFile(target)
	data[len(data)-1] ^= 0xFF // flip a payload byte -> checksum mismatch
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chunkPath(ckpt, 2), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	restored, recovered, err := Load(ckpt, 4, MaxBatchRecords)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 4 || restored.CommittedChunks() != 1 {
		t.Fatalf("rollback wrong: recovered=%d chunks=%d",
			recovered, restored.CommittedChunks())
	}
	// Corrupt and later chunks must have been removed.
	if _, err := os.Stat(chunkPath(ckpt, 1)); !os.IsNotExist(err) {
		t.Fatal("corrupt chunk should be removed on rollback")
	}
	if _, err := os.Stat(chunkPath(ckpt, 2)); !os.IsNotExist(err) {
		t.Fatal("later chunk should be removed on rollback")
	}
	// Replaying the tail plus the missing chunk restores the full index.
	for i := recovered; i < 10; i++ {
		if err := restored.Add(sampleRecord(i)); err != nil {
			t.Fatal(err)
		}
	}
	full := restored.PendingChunks()
	nextSeq := restored.CommittedChunks()
	for j, chunk := range full {
		if err := store.Commit(restored, nextSeq+j, chunk); err != nil {
			t.Fatal(err)
		}
	}
	again, n, err := Load(ckpt, 4, MaxBatchRecords)
	if err != nil {
		t.Fatal(err)
	}
	if n != 8 || again.Digest().Records != 8 {
		t.Fatalf("replay did not converge: n=%d digest=%+v", n, again.Digest())
	}
}

func TestDigestStableAcrossInstances(t *testing.T) {
	build := func() Digest {
		ix, _ := New(4, MaxBatchRecords)
		for i := 0; i < 20; i++ {
			_ = ix.Add(sampleRecord(i))
		}
		return ix.Digest()
	}
	a, b := build(), build()
	if a != b || len(a.SHA256) != 64 {
		t.Fatalf("digest mismatch or bad length: %+v %+v", a, b)
	}
}

func TestLimitEnforced(t *testing.T) {
	ix, err := New(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := ix.Add(sampleRecord(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := ix.Add(sampleRecord(3)); !mederr.HasCode(
		err, mederr.CodeLimitExceeded) {
		t.Fatalf("limit must fail, got %v", err)
	}
	if _, err := New(0, 10); err == nil {
		t.Fatal("zero chunk size must fail")
	}
	if _, err := New(4, MaxBatchRecords+1); !mederr.HasCode(
		err, mederr.CodeLimitExceeded) {
		t.Fatal("over-limit batch must fail")
	}
}

func TestTruncatedPayloadDetected(t *testing.T) {
	dir := t.TempDir()
	ckpt := filepath.Join(dir, "ckpt")
	store, _ := NewCheckpointStore(dir, ckpt)
	ix, _ := New(4, MaxBatchRecords)
	for i := 0; i < 8; i++ {
		_ = ix.Add(sampleRecord(i))
	}
	for seq, chunk := range ix.PendingChunks() {
		_ = store.Commit(ix, seq, chunk)
	}
	// Truncate the first chunk mid-payload: the declared record count
	// can no longer decode, so Load must roll back to zero chunks.
	p := chunkPath(ckpt, 0)
	data, _ := os.ReadFile(p)
	_ = os.WriteFile(p, data[:4+8], 0o644) // header + a few payload bytes
	restored, recovered, err := Load(ckpt, 4, MaxBatchRecords)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 0 || restored.CommittedChunks() != 0 {
		t.Fatalf("truncated payload must roll back fully: %d", recovered)
	}
}

func TestCheckpointPathContainment(t *testing.T) {
	root := t.TempDir()
	if _, err := NewCheckpointStore(root,
		filepath.Join(root, "..", "escape")); err == nil {
		t.Fatal("path traversal in checkpoint dir must be rejected")
	}
}
