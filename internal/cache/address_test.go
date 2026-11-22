package cache

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testSource(t *testing.T) Source {
	t.Helper()
	return Source{ID: "edge-a/rtp", Digest: KeyFromBytes([]byte("input")), Bytes: 5, Locator: "generated:input"}
}

func seedEntry(t *testing.T, s *Store, stage string) (Key, []byte) {
	t.Helper()
	result := []byte("stage-result-" + stage)
	key := KeyFromBytes(result)
	m := Manifest{
		Format: "mediapipe-cache/1", Stage: stage, Version: "v1", Key: key,
		Input:        testSource(t),
		ResultSource: Source{ID: "mediapipe/" + stage, Digest: key, Bytes: int64(len(result)), Locator: "stage"},
	}
	if err := s.Store(m, result); err != nil {
		t.Fatalf("store: %v", err)
	}
	return key, result
}

func TestCacheStoreFetchHit(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, result := seedEntry(t, s, "container")
	got, err := s.Fetch(Lookup{Key: key, Stage: "container", Version: "v1", Input: testSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Hit || !bytes.Equal(got.Bytes, result) {
		t.Fatalf("hit %+v", got)
	}
	if got.Manifest.Content != key {
		t.Fatalf("manifest content %s", got.Manifest.Content)
	}
}

func TestCacheRejectsAndQuarantinesProvenance(t *testing.T) {
	s, _ := Open(t.TempDir())
	key, _ := seedEntry(t, s, "container")
	other := testSource(t)
	other.Digest = KeyFromBytes([]byte("other input"))
	_, err := s.Fetch(Lookup{Key: key, Stage: "container", Version: "v1", Input: other})
	var q *Quarantine
	if !errors.As(err, &q) || q.Reason != ReasonInput {
		t.Fatalf("expected input quarantine, got %v", err)
	}
	names, err := s.Quarantined()
	if err != nil || len(names) == 0 {
		t.Fatalf("quarantine listing %v %v", names, err)
	}
	if _, err := s.Fetch(Lookup{Key: key, Stage: "container", Version: "v1", Input: testSource(t)}); !errors.Is(err, ErrMissing) {
		t.Fatalf("quarantined entry still readable: %v", err)
	}
}

func TestCacheRejectsVersionAndStage(t *testing.T) {
	s, _ := Open(t.TempDir())
	key, _ := seedEntry(t, s, "container")
	_, err := s.Fetch(Lookup{Key: key, Stage: "container", Version: "v2", Input: testSource(t)})
	var q *Quarantine
	if !errors.As(err, &q) || q.Reason != ReasonVersion {
		t.Fatalf("version: %v", err)
	}
	key2, _ := seedEntry(t, s, "transport")
	_, err = s.Fetch(Lookup{Key: key2, Stage: "container", Version: "v1", Input: testSource(t)})
	if !errors.As(err, &q) || q.Reason != ReasonProvenance {
		t.Fatalf("stage: %v", err)
	}
}

func TestCacheRejectsTamperedContent(t *testing.T) {
	s, _ := Open(t.TempDir())
	key, _ := seedEntry(t, s, "index")
	path := filepath.Join(s.Root(), string(key[:2]), string(key)+".bin")
	if err := os.WriteFile(path, []byte("tampered-result"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Fetch(Lookup{Key: key, Stage: "index", Version: "v1", Input: testSource(t)})
	var q *Quarantine
	if !errors.As(err, &q) || q.Reason != ReasonContent {
		t.Fatalf("tampered content accepted: %v", err)
	}
}

func TestCacheRejectsCorruptManifest(t *testing.T) {
	s, _ := Open(t.TempDir())
	key, _ := seedEntry(t, s, "sync")
	path := filepath.Join(s.Root(), string(key[:2]), string(key)+".manifest.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Fetch(Lookup{Key: key, Stage: "sync", Version: "v1", Input: testSource(t)})
	var q *Quarantine
	if !errors.As(err, &q) || q.Reason != ReasonFormat {
		t.Fatalf("corrupt manifest: %v", err)
	}
}

func TestCacheStoreRefusesUnboundContent(t *testing.T) {
	s, _ := Open(t.TempDir())
	result := []byte("xyz")
	// The entry address may be input-derived, but the producing source must
	// always declare the digest of the bytes actually stored.
	m := Manifest{Format: "mediapipe-cache/1", Stage: "s", Version: "v1",
		Key:   KeyFromBytes([]byte("entry-address")),
		Input: testSource(t),
		ResultSource: Source{ID: "x", Digest: KeyFromBytes([]byte("claimed output")),
			Bytes: int64(len(result)), Locator: "l"}}
	if err := s.Store(m, result); !errors.Is(err, ErrProvenance) {
		t.Fatalf("unbound result stored: %v", err)
	}
	// Declaring a mismatched content digest explicitly is also refused.
	m.ResultSource.Digest = KeyFromBytes(result)
	m.Content = KeyFromBytes([]byte("other"))
	if err := s.Store(m, result); !errors.Is(err, ErrContentDigest) {
		t.Fatalf("mismatched content digest stored: %v", err)
	}
}

func TestCacheStoreAcceptInputAddressedEntry(t *testing.T) {
	s, _ := Open(t.TempDir())
	result := []byte("stage-bytes")
	addr := KeyFromBytes([]byte("input-address"))
	content := KeyFromBytes(result)
	m := Manifest{Format: "mediapipe-cache/1", Stage: "transport", Version: "v1",
		Key: addr, Input: testSource(t),
		ResultSource: Source{ID: "mediapipe/transport", Digest: content,
			Bytes: int64(len(result)), Locator: "segment:1"}}
	if err := s.Store(m, result); err != nil {
		t.Fatalf("input-addressed store: %v", err)
	}
	got, err := s.Fetch(Lookup{Key: addr, Stage: "transport", Version: "v1", Input: testSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Manifest.Content != content {
		t.Fatalf("content digest %s, want %s", got.Manifest.Content, content)
	}
}

func TestKeyValidation(t *testing.T) {
	if _, err := NewKey("abc"); !errors.Is(err, ErrKeyFormat) {
		t.Fatalf("short key accepted")
	}
	if _, err := NewKey(repeat('F', 64)); !errors.Is(err, ErrKeyFormat) {
		t.Fatalf("uppercase key accepted")
	}
	if _, err := NewKey(repeat('a', 65)); !errors.Is(err, ErrKeyFormat) {
		t.Fatalf("long key accepted")
	}
	k := KeyFromBytes([]byte("x"))
	if !k.Valid() || len(k.String()) != 64 {
		t.Fatalf("derived key invalid")
	}
}

func repeat(s byte, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = s
	}
	return string(out)
}

func TestPathStaysInsideRoot(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "cache"))
	if _, err := s.pathFor("../../etc/passwd", ".bin"); !errors.Is(err, ErrKeyFormat) {
		t.Fatalf("escape key accepted: %v", err)
	}
	k := KeyFromBytes([]byte("ok"))
	p, err := s.pathFor(k, ".bin")
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(s.Root(), p); err != nil || rel[:2] == ".." {
		t.Fatalf("path escaped root: %s", p)
	}
}

func TestDigestPartsIsUnambiguous(t *testing.T) {
	a := DigestParts([]byte("ab"), []byte("c"))
	b := DigestParts([]byte("a"), []byte("bc"))
	if a == b {
		t.Fatal("length-prefixed digest collided")
	}
	if DigestParts([]byte("a")) != DigestParts([]byte("a")) {
		t.Fatal("digest not deterministic")
	}
}
