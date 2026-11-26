package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func writeOut(t *testing.T, dir, name, content string) string {
	t.Helper()
	rel := name
	full := filepath.Join(dir, rel)
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return rel
}

func TestCommitAndVerifySkipsKnownItem(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	rel := writeOut(t, dir, "frag_0001.m4s", "abc")
	item, err := s.Commit("v0#1", rel, 0)
	if err != nil {
		t.Fatal(err)
	}
	if item.SHA256 == "" || item.Size != 3 {
		t.Fatalf("committed item wrong: %+v", item)
	}
	if !s.IsVerified("v0#1") {
		t.Fatal("committed item must verify")
	}
	if s.IsVerified("v0#99") {
		t.Fatal("unknown key must not verify")
	}
	if s.Count() != 1 {
		t.Fatalf("count=%d want 1", s.Count())
	}
}

func TestResumeLoadsAndSkips(t *testing.T) {
	dir := t.TempDir()
	s1 := NewStore(dir)
	_ = s1.Load()
	rel := writeOut(t, dir, "frag_0002.m4s", "data")
	if _, err := s1.Commit("v0#2", rel, 100); err != nil {
		t.Fatal(err)
	}
	// A fresh store over the same output dir must see the committed item.
	s2 := NewStore(dir)
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	if !s2.IsVerified("v0#2") {
		t.Fatal("resume must recognize the previously verified item")
	}
	if got, _ := s2.Item("v0#2"); got.ResumeMs != 100 {
		t.Fatalf("resume_ms not restored: %+v", got)
	}
}

func TestReplacedFileInvalidatesEntry(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	_ = s.Load()
	rel := writeOut(t, dir, "frag_0003.m4s", "orig")
	if _, err := s.Commit("v0#3", rel, 0); err != nil {
		t.Fatal(err)
	}
	// Replace the committed bytes at the same path (simulating a swapped file).
	if err := os.WriteFile(filepath.Join(dir, rel), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s.IsVerified("v0#3") {
		t.Fatal("a replaced file must fail verification and drop the entry")
	}
	if s.Count() != 0 {
		t.Fatal("stale entry must have been removed")
	}
}

func TestCleanupTempDiscardsInterruptedFiles(t *testing.T) {
	dir := t.TempDir()
	// Simulate an interrupted write left behind.
	if err := os.WriteFile(filepath.Join(dir, ".tmp-frag_0004.m4s-xyz"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	removed, err := s.CleanupTemp()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed=%v want 1 temp file", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, removed[0])); !os.IsNotExist(err) {
		t.Fatal("temp file must be gone after cleanup")
	}
}

func TestBundleHashStableAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	s1 := NewStore(dir)
	_ = s1.Load()
	r1 := writeOut(t, dir, "a.m4s", "aaa")
	r2 := writeOut(t, dir, "b.m4s", "bbb")
	_, _ = s1.Commit("k#a", r1, 0)
	_, _ = s1.Commit("k#b", r2, 10)
	hash1 := s1.BundleHash()

	// Rebuild an equivalent checkpoint in a new dir with identical content.
	dir2 := t.TempDir()
	s2 := NewStore(dir2)
	_ = s2.Load()
	c1 := writeOut(t, dir2, "a.m4s", "aaa")
	c2 := writeOut(t, dir2, "b.m4s", "bbb")
	_, _ = s2.Commit("k#a", c1, 0)
	_, _ = s2.Commit("k#b", c2, 10)
	hash2 := s2.BundleHash()
	if hash1 != hash2 {
		t.Fatalf("identical artifacts must give a stable bundle hash:\n%s\n%s", hash1, hash2)
	}

	// Changing one artifact changes the bundle hash.
	os.WriteFile(filepath.Join(dir2, c2), []byte("different"), 0o600)
	s2.IsVerified("k#b") // drops the stale entry
	c2b := writeOut(t, dir2, "b.m4s", "different")
	_, _ = s2.Commit("k#b", c2b, 10)
	if s2.BundleHash() == hash1 {
		t.Fatal("a changed artifact must change the bundle hash")
	}
}

func TestMalformedCheckpointRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".checkpoint.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	if err := s.Load(); err == nil {
		t.Fatal("a malformed checkpoint must be rejected, not silently reset")
	}
}
