package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreLoadRoundTripAndIdempotence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.journal")
	j, err := NewJournal(path, "digest-A")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Store("k1", map[string]int{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordNode("k1", "repair", nil); err != nil {
		t.Fatal(err)
	}
	// Reopening with the same input digest must reuse the checkpoint.
	j2, err := NewJournal(path, "digest-A")
	if err != nil {
		t.Fatal(err)
	}
	out, ok := j2.Load("k1")
	if !ok {
		t.Fatal("checkpoint not reused after reopen")
	}
	var m map[string]int
	if err := json.Unmarshal(out.(json.RawMessage), &m); err != nil {
		t.Fatalf("payload corrupted: %v", err)
	}
	if m["x"] != 1 {
		t.Fatalf("payload value wrong: %#v", m)
	}
}

func TestDigestMismatchRejectsReuse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.journal")
	j, _ := NewJournal(path, "digest-A")
	_ = j.Store("k1", "v1")
	j2, err := NewJournal(path, "digest-B") // different input
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := j2.Load("k1"); ok {
		t.Fatal("checkpoint from another input summary must not be reused")
	}
}

func TestDependencyGating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.journal")
	j, _ := NewJournal(path, "d")
	_ = j.Store("dep", "upstream")
	_ = j.Store("child", "downstream")
	if err := j.RecordNode("child", "index", []string{"dep"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := j.Load("child"); !ok {
		t.Fatal("child with completed dependency should load")
	}
	// Simulate a journal whose dependency entry vanished.
	delete(j.data.Entries, "dep")
	_ = j.persistLocked()
	j2, _ := NewJournal(path, "d")
	if _, ok := j2.Load("child"); ok {
		t.Fatal("child must be recomputed when its dependency is missing")
	}
}

func TestCorruptJournalIsQuarantined(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.journal")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := NewJournal(path, "d")
	if err != nil {
		t.Fatalf("corrupt journal must be recoverable, got %v", err)
	}
	if j.QuarantinedPath() == "" {
		t.Fatal("corrupt journal was not quarantined")
	}
	if err := j.Store("fresh", 1); err != nil {
		t.Fatal(err)
	}
	j2, err := NewJournal(path, "d")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := j2.Load("fresh"); !ok {
		t.Fatal("fresh checkpoint after quarantine must persist")
	}
}

func TestAtomicReplacementLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.journal")
	j, _ := NewJournal(path, "d")
	for i := 0; i < 5; i++ {
		if err := j.Store("k", i); err != nil {
			t.Fatal(err)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".journal-*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}
