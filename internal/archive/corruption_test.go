package archive

import (
	"os"
	"path/filepath"
	"testing"

	"mediacore/internal/fault"
)

func TestCorruptInputDoesNotPolluteArchive(t *testing.T) {
	s := newStore(t)
	good := []byte("good-encoding")
	if _, _, err := s.Commit(Entry{Codec: "av1", Verified: true}, good); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Load()
	// Same source digest, different bytes: must be refused.
	_, _, err := s.Commit(Entry{Codec: "av1", Verified: true, Digest: DigestOf(good)}, []byte("corrupted"))
	if err == nil {
		t.Fatal("expected digest mismatch")
	}
	after, _ := s.Load()
	if len(after) != len(before) {
		t.Fatalf("manifest changed: %d -> %d", len(before), len(after))
	}
}

func TestDigestFileMatchesDigestOf(t *testing.T) {
	s := newStore(t)
	data := []byte("file-digest")
	path := filepath.Join(s.Dir, "sample.bin")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := DigestFile(path)
	if err != nil || got != DigestOf(data) {
		t.Fatalf("digest = %s err=%v", got, err)
	}
}

func TestLoadCorruptManifest(t *testing.T) {
	s := newStore(t)
	if err := os.WriteFile(filepath.Join(s.Dir, manifestName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load()
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeArchiveHashMismatch {
		t.Fatalf("err = %v", err)
	}
}

func TestManifestOrderedByDigest(t *testing.T) {
	s := newStore(t)
	for _, d := range []string{"z", "a", "m"} {
		if _, _, err := s.Put(Entry{Codec: "av1", Verified: true}, []byte("payload-"+d)); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := s.Load()
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Digest > entries[i].Digest {
			t.Fatalf("not sorted: %v", entries)
		}
	}
}
