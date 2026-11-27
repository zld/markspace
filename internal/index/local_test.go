package index

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFileTruncated(p string) error {
	return os.WriteFile(p, []byte(`[{"chunk_id":`), 0o600)
}

func TestCommitAndReopen(t *testing.T) {
	dir := t.TempDir()
	idx, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r1 := Record{ChunkID: 1, InputDigest: "a1", OutputPath: "1.pcm",
		OutputSHA256: HashBytes([]byte("out1")), StartNanos: 0, EndNanos: 2e9, SampleCount: 96}
	r0 := Record{ChunkID: 0, InputDigest: "a0", OutputPath: "0.pcm",
		OutputSHA256: HashBytes([]byte("out0")), StartNanos: 0, EndNanos: 1e9, SampleCount: 48}
	if err := idx.Commit(r1); err != nil {
		t.Fatal(err)
	}
	if err := idx.Commit(r0); err != nil {
		t.Fatal(err)
	}
	if idx.Len() != 2 {
		t.Fatalf("len = %d", idx.Len())
	}
	// Records must come back in chunk order regardless of commit order.
	got := idx.Records()
	if got[0].ChunkID != 0 || got[1].ChunkID != 1 {
		t.Fatalf("ordering wrong: %+v", got)
	}
	if _, ok := idx.Get(1); !ok {
		t.Fatal("chunk 1 missing")
	}

	// Reopen from disk and confirm persistence.
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Len() != 2 || reopened.Digest() != idx.Digest() {
		t.Fatal("reopened index does not match")
	}
}

func TestDigestStable(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(dir)
	b, _ := Open(filepath.Join(t.TempDir(), "x"))
	rec := Record{ChunkID: 7, InputDigest: "d", OutputPath: "p",
		OutputSHA256: "h", StartNanos: 5, EndNanos: 9, SampleCount: 2}
	if err := a.Commit(rec); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(rec); err != nil {
		t.Fatal(err)
	}
	if a.Digest() != b.Digest() || a.Digest() == "" {
		t.Fatal("identical records produced different/empty digests")
	}
}

func TestNoTempLeftAfterCommit(t *testing.T) {
	dir := t.TempDir()
	idx, _ := Open(dir)
	if err := idx.Commit(Record{ChunkID: 0, OutputSHA256: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("temporary index file left behind after atomic commit")
	}
}

func TestCorruptIndexRejected(t *testing.T) {
	dir := t.TempDir()
	idx, _ := Open(dir)
	if err := idx.Commit(Record{ChunkID: 0}); err != nil {
		t.Fatal(err)
	}
	// Truncate the persisted index to invalid JSON.
	p := filepath.Join(dir, "index.json")
	if err := writeFileTruncated(p); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("corrupt index was accepted")
	}
}
