package state

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStageOrderAndResume(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "task-a", "digest-1")
	if err != nil {
		t.Fatal(err)
	}
	if stage, _ := s.ResumeStage(); stage != StageIngest {
		t.Fatalf("resume = %q, want ingest", stage)
	}
	if err := s.MarkStage(StageScan, ""); err == nil {
		t.Fatal("out-of-order stage should be refused")
	}
	for _, st := range []string{StageIngest, StageTransport, StageScan} {
		if err := s.MarkStage(st, ""); err != nil {
			t.Fatalf("mark %s: %v", st, err)
		}
	}
	if stage, _ := s.ResumeStage(); stage != StageRepair {
		t.Fatalf("resume = %q, want repair", stage)
	}
	done := s.CompletedStages()
	if len(done) != 3 || done[2] != StageScan {
		t.Fatalf("done = %v", done)
	}
	if err := s.ValidateOrder(); err != nil {
		t.Fatal(err)
	}
}

func TestReopenAfterInterruption(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir, "task-b", "digest-9")
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.MarkStages(StageIngest, StageTransport); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir, "task-b", "digest-9")
	if err != nil {
		t.Fatal(err)
	}
	if stage, _ := s2.ResumeStage(); stage != StageScan {
		t.Fatalf("resume after reopen = %q, want scan", stage)
	}
}

func TestInputDigestMismatchRefused(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir, "task-c", "digest-A"); err != nil {
		t.Fatal(err)
	}
	_, err := Open(dir, "task-c", "digest-B")
	if !errors.Is(err, ErrInputMismatch) {
		t.Fatalf("err = %v, want ErrInputMismatch", err)
	}
}

func TestSnapshotWrittenAtomically(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "task-d", "d")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStage(StageIngest, "ok"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var tmpFiles int
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			tmpFiles++
		}
	}
	if tmpFiles != 0 {
		t.Fatalf("left %d temp files behind", tmpFiles)
	}
	if _, err := os.Stat(s.Path()); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	snap, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap) == 0 || snap[len(snap)-1] != '\n' {
		t.Fatal("snapshot should end with a newline")
	}
}

func TestUnknownStage(t *testing.T) {
	s, err := Open(t.TempDir(), "task-e", "d")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStage("bogus", ""); !errors.Is(err, ErrUnknownStage) {
		t.Fatalf("err = %v", err)
	}
}

func TestAllComplete(t *testing.T) {
	s, err := Open(t.TempDir(), "task-f", "d")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStages(Order...); err != nil {
		t.Fatal(err)
	}
	stage, err := s.ResumeStage()
	if err != nil || stage != "" {
		t.Fatalf("resume = %q, %v; want empty, nil", stage, err)
	}
}

func TestCorruptSnapshotRejected(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "task-g", "d")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, "task-g", "d"); err == nil {
		t.Fatal("corrupt snapshot should be rejected")
	}
}

func TestSnapshotJSONIsOrdered(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "task-h", "d")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStages(StageIngest, StageTransport); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	// Toggling an unrelated stage marker and restoring it must not reorder
	// or reformat the document.
	s2, err := Open(dir, "task-h", "d")
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.MarkStage(StageScan, "note"); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(second, []byte(`"scan": true`)) {
		t.Fatalf("scan stage not recorded: %s", second)
	}
	if !bytes.Contains(first, []byte(`"ingest": true`)) {
		t.Fatalf("first snapshot malformed: %s", first)
	}
}
