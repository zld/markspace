package archive

import (
	"os"
	"path/filepath"
	"testing"

	"mediarc/internal/model"
)

func TestCommitAndRecover(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	tx := s.Begin("va")
	tx.Add(Entry{Version: "va", Track: 1, Ticks: 0, Scale: 90000, OutOff: 0, Length: 30, Hash: "a0"})
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// recover in a new handle
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := s2.Entries()
	if len(got) != 1 || got[0].Hash != "a0" || got[0].OutOff != 0 {
		t.Fatalf("recovered %+v", got)
	}
}

func TestRollbackKeepsCommitted(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	tx := s.Begin("va")
	tx.Add(Entry{Version: "va", Track: 1, Hash: "a0"})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx2 := s.Begin("vb")
	tx2.Add(Entry{Version: "vb", Track: 1, Hash: "CONFLICT"})
	if err := tx2.Rollback(); err != nil {
		t.Fatal(err)
	}
	if s.Rollbacks() != 1 {
		t.Fatalf("rollbacks = %d, want 1", s.Rollbacks())
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after rollback: %v", err)
	}
	if got := s2.Entries(); len(got) != 1 || got[0].Version != "va" {
		t.Fatalf("committed entries damaged: %+v", got)
	}
}

func TestCorruptCheckpointUnrecoverable(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	tx := s.Begin("va")
	tx.Add(Entry{Version: "va", Track: 1, Hash: "a0"})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "checkpoint.json"), []byte(`{"count":9,"sha1":"bad"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil || err.Code != model.ErrArchive {
		t.Fatalf("want ARCHIVE error, got %v", err)
	}
}

func TestSafeJoin(t *testing.T) {
	if _, err := SafeJoin("/base", "../escape"); err == nil || err.Code != model.ErrPath {
		t.Fatalf("want PATH error, got %v", err)
	}
	if _, err := SafeJoin("/base", "/abs"); err == nil {
		t.Fatal("absolute path should be rejected")
	}
	p, err := SafeJoin("/base", "ok/file")
	if err != nil || p != "/base/ok/file" {
		t.Fatalf("got %q, %v", p, err)
	}
}
