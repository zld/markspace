package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/core"
)

func TestRecommitStageIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cp.json")
	s, _ := NewStore(dir, path)
	begin(t, s, "in", "p")
	for i := 0; i < 3; i++ {
		if err := s.Commit("ingest", []byte{byte(i)}, i); err != nil {
			t.Fatal(err)
		}
	}
	cp, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Stages) != 1 {
		t.Fatalf("stages=%d want 1", len(cp.Stages))
	}
	if cp.LastRecordID != 2 {
		t.Fatalf("cursor=%d", cp.LastRecordID)
	}
	if got := core.HashParts([]byte{2}); cp.Stages[0].Digest != got {
		t.Fatalf("digest not updated")
	}
}

func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "cp.json")
	s, _ := NewStore(dir, path)
	begin(t, s, "in", "p")
	if err := s.Commit("ingest", []byte("x"), 1); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "cp.json" {
			t.Fatalf("unexpected file %q", e.Name())
		}
	}
}

func TestOversizedCheckpointRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxCheckpointBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := LoadFile(path); err == nil {
		t.Fatal("want size rejection")
	}
}

func TestTruncatedJSONRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cp.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"input_digest":"a"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("want parse error")
	}
}

func TestDigestMismatchIsDistinguishable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cp.json")
	s, _ := NewStore(dir, path)
	begin(t, s, "in", "p")
	s2, _ := NewStore(dir, path)
	_, err := s2.Begin("other", "p")
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("err=%v", err)
	}
}
