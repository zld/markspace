package cache

import (
	"os"
	"path/filepath"
	"testing"

	"mediakernel/internal/clock"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	base := t.TempDir()
	s, err := NewStore(base, "cache")
	if err != nil {
		t.Fatal(err)
	}
	return s, base
}

func sampleEntry(key string) Entry {
	return Entry{
		SourceID:    "src-1",
		InputDigest: "in-digest",
		IndexDigest: "idx-digest",
		Key:         key,
	}
}

func TestKeyDeterminismAndParams(t *testing.T) {
	p := Params{Channels: 2, SampleRate: 48000, BlockFrames: 480, ClockRate: 48000,
		Layout: "2ch-48000Hz", StageVersion: "v1"}
	k1 := Key("abc", p)
	k2 := Key("abc", p)
	if k1 != k2 || len(k1) != 64 {
		t.Fatal("key must be a stable 64-char hex digest")
	}
	if Key("abd", p) == k1 {
		t.Fatal("different input digest must change key")
	}
	p2 := p
	p2.BlockFrames = 240
	if Key("abc", p2) == k1 {
		t.Fatal("different params must change key")
	}
	// Map-based hashing is order-independent.
	m := map[string]string{"b": "2", "a": "1"}
	rev := map[string]string{"a": "1", "b": "2"}
	if KeyFromMap("x", m) != KeyFromMap("x", rev) {
		t.Fatal("map key hashing must be order-independent")
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	key := Key("in-digest", Params{Channels: 1, SampleRate: 8000, StageVersion: "v1"})
	blob := []byte("index-bytes")
	if err := s.Put(sampleEntry(key), blob); err != nil {
		t.Fatal(err)
	}
	got, e, err := s.Get(key, "src-1", "in-digest", "idx-digest")
	if err != nil {
		t.Fatalf("valid cache entry rejected: %v", err)
	}
	if string(got) != "index-bytes" || e.ArtifactDigest != DigestBytes(blob) {
		t.Fatal("cache content or digest wrong")
	}
}

func TestCorruptedSourceIdentityDiscards(t *testing.T) {
	s, _ := newTestStore(t)
	key := Key("in-digest", Params{StageVersion: "v1"})
	if err := s.Put(sampleEntry(key), []byte("blob")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get(key, "attacker-src", "in-digest", "idx-digest"); err == nil {
		t.Fatal("foreign source identity must invalidate the entry")
	} else if ce, ok := clock.AsError(err); !ok || ce.Category != clock.CatCacheMismatch {
		t.Fatalf("want cache_mismatch, got %v", err)
	}
	// After discard the files must be gone.
	metaPath, blobPath, _ := s.paths(key)
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Fatal("meta file should be removed after discard")
	}
	if _, err := os.Stat(blobPath); !os.IsNotExist(err) {
		t.Fatal("blob file should be removed after discard")
	}
}

func TestDigestMismatchDiscards(t *testing.T) {
	s, _ := newTestStore(t)
	key := Key("in-digest", Params{StageVersion: "v1"})
	if err := s.Put(sampleEntry(key), []byte("blob")); err != nil {
		t.Fatal(err)
	}
	// Wrong input digest behaves identically to a corrupt source.
	if _, _, err := s.Get(key, "src-1", "tampered-input", "idx-digest"); err == nil {
		t.Fatal("input digest mismatch must invalidate")
	}
	if _, _, err := s.Get(key, "src-1", "in-digest", "other-index"); err == nil {
		t.Fatal("index digest mismatch must invalidate")
	}
}

func TestTamperedBlobDetected(t *testing.T) {
	s, _ := newTestStore(t)
	key := Key("in-digest", Params{StageVersion: "v1"})
	if err := s.Put(sampleEntry(key), []byte("blob")); err != nil {
		t.Fatal(err)
	}
	_, blobPath, _ := s.paths(key)
	// Overwrite the blob without updating the recorded artifact digest.
	if err := os.WriteFile(blobPath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get(key, "src-1", "in-digest", "idx-digest"); err == nil {
		t.Fatal("tampered blob must fail artifact digest verification")
	} else if ce, ok := clock.AsError(err); !ok || ce.Category != clock.CatCacheMismatch {
		t.Fatalf("want cache_mismatch, got %v", err)
	}
}

func TestSandboxRejectsEscape(t *testing.T) {
	base := t.TempDir()
	if _, err := NewStore(base, filepath.Join("..", "escape")); err == nil {
		t.Fatal("cache directory outside sandbox must be rejected")
	}
}
