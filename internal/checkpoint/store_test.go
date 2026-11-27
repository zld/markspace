package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediapipeline/internal/fault"
)

func open(t *testing.T, dir string) (*Store, *fault.Report) {
	t.Helper()
	var rep fault.Report
	s, err := Open(filepath.Join(dir, "state.json"), &rep)
	if err != nil {
		t.Fatal(err)
	}
	return s, &rep
}

func TestCommitIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s, rep := open(t, dir)
	committed, err := s.Commit("u0", "aa", 10)
	if err != nil || !committed {
		t.Fatalf("first commit: %v %v", committed, err)
	}
	committed, err = s.Commit("u0", "aa", 10)
	if err != nil || committed {
		t.Fatalf("replay must be a no-op: %v %v", committed, err)
	}
	if s.Len() != 1 || s.Commits() != 1 {
		t.Fatalf("len=%d commits=%d", s.Len(), s.Commits())
	}
	if rep.Count() != 0 {
		t.Fatalf("unexpected faults: %v", rep.All())
	}
}

func TestCommitDigestConflict(t *testing.T) {
	dir := t.TempDir()
	s, rep := open(t, dir)
	if _, err := s.Commit("u0", "aa", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit("u0", "bb", 10); err == nil {
		t.Fatal("different digest for same key must fail")
	}
	if !rep.Has(fault.CodeCheckpointBad) {
		t.Fatalf("conflict not reported: %v", rep.All())
	}
}

func TestReopenPreservesRecords(t *testing.T) {
	dir := t.TempDir()
	s, _ := open(t, dir)
	for _, k := range []string{"u0", "u1", "u2"} {
		if _, err := s.Commit(k, "d-"+k, 1); err != nil {
			t.Fatal(err)
		}
	}
	var rep fault.Report
	s2, err := Open(filepath.Join(dir, "state.json"), &rep)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Len() != 3 || !s2.Done("u1") || s2.Commits() != 0 {
		t.Fatalf("reopened store wrong: len=%d commits=%d", s2.Len(), s2.Commits())
	}
	recs := s2.Records()
	if recs[0].Seq != 1 || recs[2].Seq != 3 {
		t.Fatalf("commit order lost: %+v", recs)
	}
}

func TestCorruptChecksumRejected(t *testing.T) {
	dir := t.TempDir()
	s, _ := open(t, dir)
	if _, err := s.Commit("u0", "aa", 10); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte inside a record digest; the CRC no longer matches.
	tampered := strings.Replace(string(data), `"aa"`, `"ab"`, 1)
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	var rep fault.Report
	if _, err := Open(path, &rep); err == nil {
		t.Fatal("tampered checkpoint must be rejected")
	}
	if !rep.Has(fault.CodeCheckpointBad) {
		t.Fatalf("corruption not reported: %v", rep.All())
	}
}

func TestMalformedJSONRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	var rep fault.Report
	if _, err := Open(path, &rep); err == nil {
		t.Fatal("malformed checkpoint must be rejected")
	}
	if !rep.Has(fault.CodeCheckpointBad) {
		t.Fatalf("faults = %v", rep.All())
	}
}

func TestRollbackDropsLaterRecords(t *testing.T) {
	dir := t.TempDir()
	s, _ := open(t, dir)
	for _, k := range []string{"u0", "u1", "u2", "u3"} {
		if _, err := s.Commit(k, "d", 1); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := s.Rollback(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 || removed[0].Key != "u2" || removed[1].Key != "u3" {
		t.Fatalf("removed = %+v", removed)
	}
	if s.Len() != 2 || s.Done("u2") {
		t.Fatalf("rollback left state wrong: len=%d", s.Len())
	}
	// Persisted: a fresh open sees only the retained prefix.
	var rep fault.Report
	s2, err := Open(filepath.Join(dir, "state.json"), &rep)
	if err != nil || s2.Len() != 2 {
		t.Fatalf("persisted rollback: len=%d err=%v", s2.Len(), err)
	}
}

func TestMissingFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	s, rep := open(t, dir)
	if s.Len() != 0 || rep.Count() != 0 {
		t.Fatalf("missing file must start clean: %v", rep.All())
	}
}

func TestNilReportRejected(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "x.json"), nil); err == nil {
		t.Fatal("expected nil-report error")
	}
}
