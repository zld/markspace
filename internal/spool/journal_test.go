package spool

import (
	"path/filepath"
	"testing"

	"mediakernel/internal/verify"
)

func TestJournalAppendLoadCover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	e1 := Entry{Offset: 0, Length: 100, SHA256: verify.Sum256Bytes([]byte("a"))}
	e2 := Entry{Offset: 100, Length: 50, SHA256: verify.Sum256Bytes([]byte("b"))}
	if err := j.Append(e1); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(e2); err != nil {
		t.Fatal(err)
	}
	j.Close()

	// Simulate process restart: reload from disk.
	j2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j2.Close()
	if got := j2.Entries(); len(got) != 2 || got[0] != e1 || got[1] != e2 {
		t.Fatalf("entries=%v", got)
	}
	if !j2.Covers(0, 150) {
		t.Fatal("should cover [0,150)")
	}
	if j2.Covers(0, 151) || j2.Covers(50, 200) {
		t.Fatal("must not cover beyond committed ranges")
	}
}

func TestJournalSurvivesEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if j.Covers(0, 1) {
		t.Fatal("empty journal covers nothing")
	}
}
