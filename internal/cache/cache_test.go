package cache

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/timeline"
)

func TestDerivedPutGetAndReuse(t *testing.T) {
	s, _ := New(t.TempDir())
	p := Params{TargetKbps: 800, Pipeline: "v1"}
	key, err := DerivedKey("content-hash-abc", p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Has(key) {
		t.Fatal("fresh cache must miss")
	}
	data := []byte("derived segment bytes")
	if err := s.Put(key, data); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	saved, _ := os.ReadFile(got)
	if string(saved) != string(data) {
		t.Fatal("cached content mismatch")
	}
	// Identical content+params yields the same key (cache hit across runs).
	key2, _ := DerivedKey("content-hash-abc", p)
	if key != key2 || !s.Has(key2) {
		t.Fatal("identical derivation must reuse one entry")
	}
	// Different params must produce a different key.
	keyOther, _ := DerivedKey("content-hash-abc", Params{TargetKbps: 1200, Pipeline: "v1"})
	if keyOther == key {
		t.Fatal("different processing params must not share a key")
	}
}

func TestDerivedChecksumMismatchIsMiss(t *testing.T) {
	s, _ := New(t.TempDir())
	key, _ := DerivedKey("h", Params{TargetKbps: 1, Pipeline: "v"})
	if err := s.Put(key, []byte("good")); err != nil {
		t.Fatal(err)
	}
	// Corrupt the data segment behind the sidecar.
	path, _ := s.Get(key)
	if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s.Has(key) {
		t.Fatal("corrupt segment must be treated as a cache miss")
	}
	if _, err := s.Get(key); err != ErrChecksum {
		t.Fatalf("expected ErrChecksum, got %v", err)
	}
}

func TestTrackContentHashRewrapReuse(t *testing.T) {
	// Two "containers" differ in wrapper bytes but expose identical sample
	// ranges. The track content digest must be equal, enabling cache reuse.
	sample := []byte("identical sample payload")
	mkBlob := func(prefix []byte) string {
		dir := t.TempDir()
		p := filepath.Join(dir, "blob.bin")
		os.WriteFile(p, append(append(append([]byte{}, prefix...), sample...), []byte("trailer")...), 0o644)
		return p
	}
	b1 := mkBlob([]byte("container-A-moov"))
	b2 := mkBlob([]byte("container-B-different-moov"))
	f1, _ := os.Open(b1)
	f2, _ := os.Open(b2)
	defer f1.Close()
	defer f2.Close()
	r1 := []ByteRange{{Offset: int64(len("container-A-moov")), Length: int64(len(sample))}}
	r2 := []ByteRange{{Offset: int64(len("container-B-different-moov")), Length: int64(len(sample))}}
	h1, err := TrackContentHash(f1, r1)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := TrackContentHash(f2, r2)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("identical track content across rewraps must hash equal")
	}
	// A changed sample range changes the digest.
	h3, _ := TrackContentHash(f1, []ByteRange{{Offset: 0, Length: 5}})
	if h3 == h1 {
		t.Fatal("different byte range must change the content hash")
	}
}

func TestIndexPersistenceAndReuseRules(t *testing.T) {
	s, _ := New(t.TempDir())
	missing, err := s.LoadIndex("nonexistenthash0000000000000000000000000000")
	if err != nil || missing != nil {
		t.Fatalf("missing index should be nil,nil got %v %v", missing, err)
	}
	idx := &timeline.Index{
		SourceHash: "src1", MetadataHash: "meta1", ParserVersion: "mediad-parser-1",
		CacheKey: "cache-1",
	}
	if err := s.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadIndex("src1")
	if err != nil || loaded.MetadataHash != "meta1" {
		t.Fatalf("load index: %+v err=%v", loaded, err)
	}
	// Metadata-only edit: source hash changed (new container) but cache key
	// unchanged because track content+params are identical => reuse derived.
	metaEdited := &timeline.Index{
		SourceHash: "src2", MetadataHash: "meta2", ParserVersion: "mediad-parser-1",
		CacheKey: "cache-1",
	}
	if !CanReuseDerived(idx, metaEdited) {
		t.Fatal("metadata-only edit must allow derived cache reuse")
	}
	if !timeline.NeedsRebuild(idx, metaEdited) {
		t.Fatal("index must still rebuild when metadata hash changes")
	}
	// Real content change alters the cache key => no reuse.
	contentChanged := &timeline.Index{
		SourceHash: "src3", MetadataHash: "meta3", ParserVersion: "mediad-parser-1",
		CacheKey: "cache-2",
	}
	if CanReuseDerived(idx, contentChanged) {
		t.Fatal("content change must invalidate derived cache")
	}
}

func TestIndexDigestStable(t *testing.T) {
	idx := &timeline.Index{SourceHash: "a", MetadataHash: "b", ParserVersion: "p", CacheKey: "c"}
	d1, _ := IndexDigest(idx)
	d2, _ := IndexDigest(idx)
	if d1 != d2 || d1 == "" {
		t.Fatal("index digest must be stable and non-empty")
	}
}
