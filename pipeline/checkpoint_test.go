package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.Load(); found {
		t.Fatal("empty store must not find a checkpoint")
	}
	c1 := Checkpoint{Seq: 0, Stage: "ingest", Offset: 100, Digest: "abc", ColorState: "stable"}
	if err := s.Save(c1); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.Load()
	if err != nil || !found || got.Offset != 100 || got.Stage != "ingest" {
		t.Fatalf("got %+v found=%v err=%v", got, found, err)
	}
	c2 := Checkpoint{Seq: 1, Stage: "audio", Offset: 200, Digest: "def", ColorState: "stable"}
	if err := s.Save(c2); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Load()
	if got.Seq != 1 || got.Offset != 200 {
		t.Fatalf("latest not preferred: %+v", got)
	}
}

func TestTruncatedSlotFallsBack(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	c0 := Checkpoint{Seq: 0, Stage: "ingest", Offset: 100, Digest: "abc", ColorState: "stable"}
	c1 := Checkpoint{Seq: 1, Stage: "audio", Offset: 200, Digest: "def", ColorState: "stable"}
	if err := s.Save(c0); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(c1); err != nil {
		t.Fatal(err)
	}
	// simulate a crash mid-write: truncate the newer slot
	p := filepath.Join(dir, "checkpoint.b.json")
	data, _ := os.ReadFile(p)
	if err := os.WriteFile(p, data[:len(data)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.Load()
	if err != nil || !found {
		t.Fatalf("recovery failed: %+v %v", found, err)
	}
	if got.Seq != 0 || got.Offset != 100 {
		t.Fatalf("fell back to %+v, want seq 0 offset 100", got)
	}
}

func TestTamperedChecksumRejected(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	c := Checkpoint{Seq: 0, Stage: "ingest", Offset: 100, Digest: "abc", ColorState: "stable"}
	if err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	// flip a byte in the digest field of the only slot
	p := filepath.Join(dir, "checkpoint.a.json")
	data, _ := os.ReadFile(p)
	for i, b := range data {
		if b == 'a' {
			data[i] = 'z'
			break
		}
	}
	os.WriteFile(p, data, 0o644)
	if _, found, _ := s.Load(); found {
		t.Fatal("tampered checkpoint must be rejected")
	}
}
