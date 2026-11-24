package checkpoint

import (
	"path/filepath"
	"testing"

	"mediapipeline/internal/dag"
)

func TestOpenAbsentThenSaveReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.json")
	s, err := Open(path, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("parse", dag.State{Status: "done", InputDigest: "in", ConfigDigest: "cfg"}); err != nil {
		t.Fatal(err)
	}
	s.AppendInterval("audio", Interval{Start: 0, End: 960})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	// Reopen: state must survive, simulating an interrupted run.
	s2, err := Open(path, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	st, ok := s2.Get("parse")
	if !ok || st.Status != "done" || st.InputDigest != "in" {
		t.Fatalf("reloaded state = %+v ok=%v", st, ok)
	}
	if ivs := s2.Intervals("audio"); len(ivs) != 1 || ivs[0].End != 960 {
		t.Fatalf("reloaded intervals = %+v", ivs)
	}
}

func TestRunIDMismatchRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.json")
	s, _ := Open(path, "run-A")
	_ = s.Save()
	if _, err := Open(path, "run-B"); err == nil {
		t.Fatal("run id mismatch must be rejected to avoid cross-run reuse")
	}
}

func TestResumeSkipsCompletedNodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.json")
	// First run completes only "a".
	s, _ := Open(path, "run")
	_ = s.Put("a", dag.State{Status: "done", InputDigest: "ia", ConfigDigest: "ca"})
	_ = s.Save()

	// Second run: the scheduler sees "a" already done.
	s2, _ := Open(path, "run")
	if st, ok := s2.Get("a"); !ok || st.Status != "done" {
		t.Fatalf("node a should be reusable: %+v", st)
	}
	if _, ok := s2.Get("b"); ok {
		t.Fatal("node b must not exist before it runs")
	}
}

func TestIntervalSortedSnapshot(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "cp.json"), "r")
	s.AppendInterval("audio", Interval{Start: 960, End: 1920})
	s.AppendInterval("audio", Interval{Start: 0, End: 960})
	ivs := s.Intervals("audio")
	if ivs[0].Start != 0 || ivs[1].Start != 960 {
		t.Fatalf("intervals not sorted: %+v", ivs)
	}
	snap := s.Snapshot()
	snap.Nodes["x"] = dag.State{Status: "done"}
	if _, leak := s.Get("x"); leak {
		t.Fatal("snapshot must be a deep copy")
	}
}

func TestEmptyNodeNameRejected(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "cp.json"), "r")
	if err := s.Put("", dag.State{}); err == nil {
		t.Fatal("empty node name must be rejected")
	}
}
