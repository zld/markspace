package archive

import (
	"os"
	"path/filepath"
	"testing"

	"medialib/internal/fingerprint"
)

func TestCommitAndVerify(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("stage-one-output")
	h := fingerprint.HashBytes(data)
	reused, err := s.Commit("audio", "a.pcm", data, h)
	if err != nil || reused {
		t.Fatalf("reused=%v err=%v", reused, err)
	}
	ok, err := s.Verify("a.pcm", h)
	if err != nil || !ok {
		t.Fatalf("verify=%v err=%v", ok, err)
	}
	if s.LastCommitted() != "a.pcm" {
		t.Fatalf("last=%s", s.LastCommitted())
	}
	s.Close()
}

func TestRecoveryRollsBackUncommitted(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	good := []byte("committed")
	gh := fingerprint.HashBytes(good)
	if _, err := s.Commit("ingest", "good.pcm", good, gh); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteUncommitted("audio", "partial.pcm", []byte("half")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	// Reopen: partial.pcm.tmp must be rolled back, good.pcm kept.
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.RolledBack != 1 {
		t.Fatalf("rolled back %d", s2.RolledBack)
	}
	if _, err := os.Stat(filepath.Join(dir, "partial.pcm.tmp")); !os.IsNotExist(err) {
		t.Fatal("temp file survived recovery")
	}
	if ok, _ := s2.Verify("good.pcm", gh); !ok {
		t.Fatal("committed artifact lost")
	}
	if s2.LastCommitted() != "good.pcm" {
		t.Fatal("resume boundary wrong")
	}
}

func TestDuplicateMaterialReused(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	defer s.Close()
	data := []byte("same-content")
	h := fingerprint.HashBytes(data)
	if _, err := s.Commit("audio", "x.pcm", data, h); err != nil {
		t.Fatal(err)
	}
	reused, err := s.Commit("audio", "y.pcm", data, h)
	if err != nil || !reused {
		t.Fatalf("reused=%v err=%v", reused, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "y.pcm")); !os.IsNotExist(err) {
		t.Fatal("duplicate material rewritten")
	}
}

func TestCorruptCommitRejected(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	defer s.Close()
	if _, err := s.Commit("audio", "z.pcm", []byte("bad"), "00"); err == nil {
		t.Fatal("corrupt commit accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "z.pcm")); !os.IsNotExist(err) {
		t.Fatal("corrupt artifact archived")
	}
}
