package checkpoint

import (
	"os"
	"path/filepath"
	"testing"

	"pwf/internal/dag"
	"pwf/internal/media"
)

func TestCommitAndReuse(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.RecordDigests("node1", "in", "cfg")
	sha, err := s.Commit("node1", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) != 64 {
		t.Fatalf("sha256 must be 64 hex chars, got %d", len(sha))
	}
	// Matching digests -> reusable.
	got, ok := s.Reusable("node1", "in", "cfg")
	if !ok || got != sha {
		t.Fatal("matching node should be reusable")
	}
	// Changed config digest -> must recompute.
	if _, ok := s.Reusable("node1", "in", "changed"); ok {
		t.Fatal("stale config digest must invalidate reuse")
	}
}

func TestTamperedOutputForcesRecompute(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	s.RecordDigests("n", "in", "cfg")
	if _, err := s.Commit("n", []byte("data")); err != nil {
		t.Fatal(err)
	}
	// Tamper with the committed output file.
	out := filepath.Join(dir, "checkpoint", "outputs", "n.out")
	if err := os.WriteFile(out, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Reusable("n", "in", "cfg"); ok {
		t.Fatal("tampered output must not be reusable")
	}
	// Stale record should have been evicted.
	if _, ok := s.Reusable("n", "in", "cfg"); ok {
		t.Fatal("evicted record must stay non-reusable after re-check")
	}
}

func TestAtomicCommitLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if _, err := s.Commit("a", []byte("x")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "checkpoint", "outputs"))
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatal("temporary output must not remain after commit")
		}
	}
}

func TestPathEscapeRejected(t *testing.T) {
	s, _ := Open(t.TempDir())
	_, err := s.Commit("../escape", []byte("x"))
	me, ok := media.AsError(err)
	if !ok || me.Kind != media.KindPathEscape {
		t.Fatalf("expected path-escape error, got %v", err)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s1, _ := Open(dir)
	s1.RecordDigests("persist", "in", "cfg")
	sha, _ := s1.Commit("persist", []byte("keep"))

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.Reusable("persist", "in", "cfg")
	if !ok || got != sha {
		t.Fatal("checkpoint state must survive reopening the store")
	}
	states := s2.States()
	if len(states) != 1 || states[0].Status != string(dag.StatusSucceeded) {
		t.Fatalf("reloaded states wrong: %+v", states)
	}
}

func TestCorruptIndexDiscarded(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_, _ = s.Commit("x", []byte("y"))
	// Corrupt the index JSON; Open must not fatal, node simply recomputes.
	if err := os.WriteFile(filepath.Join(dir, "checkpoint", "index.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("corrupt index must be tolerated: %v", err)
	}
	if _, ok := s2.Reusable("x", "", ""); ok {
		t.Fatal("corrupt index means no node is reusable")
	}
}
