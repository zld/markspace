package pipeline

import (
	"errors"
	"os"
	"testing"
)

func TestCheckpointResumeAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	versions := map[string]int{"scan": 1, "manifest": 1}

	cp, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Bind("in-hash", "cfg-hash", versions); err != nil {
		t.Fatal(err)
	}
	if cp.Done("scan") {
		t.Fatal("fresh checkpoint must not report done")
	}
	if err := cp.Record("scan"); err != nil {
		t.Fatal(err)
	}

	// Reload from disk: same input/config -> resume from recorded state.
	cp2, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cp2.Bind("in-hash", "cfg-hash", versions); err != nil {
		t.Fatal(err)
	}
	if !cp2.Done("scan") || cp2.Done("manifest") {
		t.Fatalf("completed=%v", cp2.Completed)
	}
}

func TestCheckpointRefusesMismatch(t *testing.T) {
	dir := t.TempDir()
	cp, _ := LoadCheckpoint(dir)
	if err := cp.Bind("in-hash", "cfg-hash", map[string]int{"scan": 1}); err != nil {
		t.Fatal(err)
	}
	if err := cp.Record("scan"); err != nil {
		t.Fatal(err)
	}

	reload := func() *Checkpoint {
		c, err := LoadCheckpoint(dir)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if err := reload().Bind("other-input", "cfg-hash", map[string]int{"scan": 1}); !errors.Is(err, ErrInputChanged) {
		t.Fatalf("input mismatch: %v", err)
	}
	if err := reload().Bind("in-hash", "other-cfg", map[string]int{"scan": 1}); !errors.Is(err, ErrConfigChanged) {
		t.Fatalf("config mismatch: %v", err)
	}
	if err := reload().Bind("in-hash", "cfg-hash", map[string]int{"scan": 2}); !errors.Is(err, ErrVersionChanged) {
		t.Fatalf("version mismatch: %v", err)
	}
}

func TestCorruptCheckpointFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadCheckpoint(dir); err != nil { // ensure dir usage is valid
		t.Fatal(err)
	}
	if err := os.WriteFile(CheckpointPath(dir), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpoint(dir); err == nil {
		t.Fatal("expected corrupt checkpoint error")
	}
}
