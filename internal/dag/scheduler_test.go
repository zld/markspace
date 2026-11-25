package dag

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"mediad/internal/model"
)

func newTestCheckpoint(t *testing.T) *Checkpoint {
	t.Helper()
	cp, err := LoadCheckpoint(filepath.Join(t.TempDir(), "checkpoint.json"))
	if !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("missing file should be ErrNotFound, got %v", err)
	}
	return cp
}

func TestCheckpointNodeAndOffsetRoundTrip(t *testing.T) {
	cp := newTestCheckpoint(t)
	if err := cp.MarkNode("parse", StateSucceeded); err != nil {
		t.Fatal(err)
	}
	if err := cp.SetInputOffset("parse", 1880); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadCheckpoint(cp.Path())
	if err != nil {
		t.Fatal(err)
	}
	if st, ok := reloaded.NodeState("parse"); !ok || st != StateSucceeded {
		t.Fatalf("node state = %q %v", st, ok)
	}
	if off, ok := reloaded.InputOffset("parse"); !ok || off != 1880 {
		t.Fatalf("offset = %d %v", off, ok)
	}
	if reloaded.Generation() < 2 {
		t.Fatalf("generation = %d, want >= 2", reloaded.Generation())
	}
}

func TestCommitSegmentIdempotent(t *testing.T) {
	cp := newTestCheckpoint(t)
	commit := SegmentCommit{Key: TaskKey("high", 3), File: "high-seg-00003.m4s",
		SHA256: "abc", ByteSize: 42, Index: 3, Rep: "high"}
	first, added, err := cp.CommitSegment(commit)
	if err != nil || !added || first.Key != commit.Key {
		t.Fatalf("first commit: added=%v err=%v", added, err)
	}
	second, added, err := cp.CommitSegment(commit)
	if err != nil || added || second.SHA256 != "abc" {
		t.Fatalf("duplicate commit must be idempotent: added=%v err=%v", added, err)
	}
	if !cp.IsCommitted(TaskKey("high", 3)) {
		t.Fatal("ledger lookup failed")
	}
	if len(cp.Commits()) != 1 {
		t.Fatalf("ledger size = %d, want 1", len(cp.Commits()))
	}
}

func TestSchedulerRunsInDependencyOrder(t *testing.T) {
	cp := newTestCheckpoint(t)
	s := NewScheduler(cp)
	var order []string
	mk := func(key string, deps ...string) Node {
		return Node{Key: key, Deps: deps, Run: func(ctx context.Context, _ *Checkpoint) error {
			order = append(order, key)
			return nil
		}}
	}
	s.Add(mk("parse"))
	s.Add(mk("segment", "parse"))
	s.Add(mk("manifest", "segment"))
	results, err := s.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"parse", "segment", "manifest"}
	for i, r := range results {
		if r.Key != want[i] || r.State != StateSucceeded {
			t.Fatalf("result %d = %+v", i, r)
		}
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("execution order = %v", order)
		}
	}
}

func TestSchedulerSkipsCompletedAndReportsFirstBlocker(t *testing.T) {
	cp := newTestCheckpoint(t)
	if err := cp.MarkNode("parse", StateSucceeded); err != nil {
		t.Fatal(err)
	}
	var ran int32
	s := NewScheduler(cp)
	s.Add(Node{Key: "parse", Run: func(context.Context, *Checkpoint) error {
		atomic.AddInt32(&ran, 1)
		return nil
	}})
	boom := errors.New("boom")
	s.Add(Node{Key: "segment", Deps: []string{"parse"}, Run: func(context.Context, *Checkpoint) error {
		atomic.AddInt32(&ran, 1)
		return boom
	}})
	s.Add(Node{Key: "manifest", Deps: []string{"segment"}, Run: func(context.Context, *Checkpoint) error {
		atomic.AddInt32(&ran, 1)
		return nil
	}})
	results, err := s.Execute(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
	// parse is already succeeded in the checkpoint so it does not run;
	// segment runs and fails; manifest must be blocked, not executed.
	if atomic.LoadInt32(&ran) != 1 {
		t.Fatalf("ran = %d, want 1 (only segment runs)", ran)
	}
	last := results[len(results)-1]
	if last.Key != "manifest" || last.Blocker != "segment" || !errors.Is(last.Err, model.ErrBlocked) {
		t.Fatalf("downstream node result = %+v", last)
	}
}

func TestCheckpointRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cp.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpoint(path); err == nil {
		t.Fatal("corrupt checkpoint must return an error, not silently reset")
	}
}
