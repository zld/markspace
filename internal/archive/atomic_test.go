package archive

import (
	"os"
	"testing"

	"mediacore/internal/fault"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPutIsIdempotent(t *testing.T) {
	s := newStore(t)
	data := []byte("encoded-av1-bytes")
	e1, dup, err := s.Put(Entry{Codec: "av1", Verified: true}, data)
	if err != nil {
		t.Fatal(err)
	}
	if dup {
		t.Fatal("first put must not be duplicate")
	}
	e2, dup, err := s.Put(Entry{Codec: "av1", Verified: true}, data)
	if err != nil {
		t.Fatal(err)
	}
	if !dup || e1.Digest != e2.Digest {
		t.Fatalf("redelivery = %+v dup=%v", e2, dup)
	}
	entries, _ := s.Load()
	if len(entries) != 1 {
		t.Fatalf("entries = %d want 1", len(entries))
	}
	blob, err := os.ReadFile(s.BlobPath(e1))
	if err != nil || string(blob) != string(data) {
		t.Fatalf("blob = %q err=%v", blob, err)
	}
}

func TestPutRejectsBlobMismatch(t *testing.T) {
	s := newStore(t)
	_, _, err := s.Put(Entry{Codec: "av1", BlobDigest: DigestOf([]byte("other"))}, []byte("data"))
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeArchiveHashMismatch {
		t.Fatalf("err = %v", err)
	}
}

func TestCommitRequiresVerified(t *testing.T) {
	s := newStore(t)
	_, _, err := s.Commit(Entry{Codec: "av1", Verified: false}, []byte("data"))
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeArchiveUnverified {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := s.Load(); len(entries) != 0 {
		t.Fatal("unverified asset must not be archived")
	}
}
