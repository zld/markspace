package dag

import (
	"errors"
	"testing"

	mpierr "mediapipeline/internal/errors"
)

// memStore is an in-memory StateStore for scheduler tests.
type memStore struct{ m map[string]State }

func (s *memStore) Get(node string) (State, bool) {
	v, ok := s.m[node]
	return v, ok
}
func (s *memStore) Put(node string, st State) error {
	s.m[node] = st
	return nil
}

func newMemStore() *memStore { return &memStore{m: map[string]State{}} }

func linearGraph(t *testing.T) *Graph {
	t.Helper()
	g, err := New([]Node{
		{Name: "a"},
		{Name: "b", Deps: []string{"a"}},
		{Name: "c", Deps: []string{"b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestRunAllNodes(t *testing.T) {
	ran := []string{}
	g := linearGraph(t)
	s := NewScheduler(g, newMemStore(), map[string]NodeFunc{
		"a": func(string) error { ran = append(ran, "a"); return nil },
		"b": func(string) error { ran = append(ran, "b"); return nil },
		"c": func(string) error { ran = append(ran, "c"); return nil },
	})
	rep := s.Run()
	if rep.FailedNode != "" || rep.Blocked != nil {
		t.Fatalf("unexpected failure: %+v", rep)
	}
	if len(ran) != 3 || ran[0] != "a" || ran[2] != "c" {
		t.Fatalf("execution order = %v", ran)
	}
}

func TestResumeRerunsOnlyIncompleteNodes(t *testing.T) {
	g := linearGraph(t)
	store := newMemStore()
	// Pretend a previous run completed nodes a and b with idempotency data.
	_ = store.Put("a", State{Status: "done", InputDigest: "ia", ConfigDigest: "ca"})
	_ = store.Put("b", State{Status: "done", InputDigest: "ib", ConfigDigest: "cb"})

	ran := []string{}
	s := NewScheduler(g, store, map[string]NodeFunc{
		"a": func(string) error { ran = append(ran, "a"); return nil },
		"b": func(string) error { ran = append(ran, "b"); return nil },
		"c": func(string) error { ran = append(ran, "c"); return nil },
	})
	rep := s.Run()
	if len(ran) != 1 || ran[0] != "c" {
		t.Fatalf("only c should run, got %v", ran)
	}
	if rep.ReusedCount() != 2 {
		t.Fatalf("reused = %d, want 2", rep.ReusedCount())
	}
}

func TestFailureReportsFirstBlockingDependency(t *testing.T) {
	g := linearGraph(t)
	s := NewScheduler(g, newMemStore(), map[string]NodeFunc{
		"a": func(string) error {
			return mpierr.New(mpierr.CatCorrupt, 5, "a", "bad frame")
		},
		"b": func(string) error { return nil },
		"c": func(string) error { return nil },
	})
	rep := s.Run()
	if rep.FailedNode != "a" {
		t.Fatalf("failed node = %q, want a", rep.FailedNode)
	}
	if rep.Blocked == nil || rep.Blocked.Node != "b" || rep.Blocked.Dependency != "a" {
		t.Fatalf("blocked report = %+v", rep.Blocked)
	}
	if rep.FailedErr == nil || rep.FailedErr.Category != mpierr.CatCorrupt || rep.FailedErr.FirstSeq != 5 {
		t.Fatalf("failed err = %+v", rep.FailedErr)
	}
}

func TestNonMediaErrorWrapped(t *testing.T) {
	g, _ := New([]Node{{Name: "x"}})
	s := NewScheduler(g, newMemStore(), map[string]NodeFunc{
		"x": func(string) error { return errors.New("boom") },
	})
	rep := s.Run()
	if rep.FailedErr == nil || rep.FailedErr.Node != "x" {
		t.Fatalf("expected wrapped media error, got %+v", rep.FailedErr)
	}
}

func TestMarkDoneIdempotencyKey(t *testing.T) {
	g := linearGraph(t)
	store := newMemStore()
	s := NewScheduler(g, store, nil)
	if err := s.MarkDone("a", "in", "cfg"); err != nil {
		t.Fatal(err)
	}
	st, ok := store.Get("a")
	if !ok || st.Status != "done" || st.InputDigest != "in" || st.ConfigDigest != "cfg" {
		t.Fatalf("stored state = %+v", st)
	}
	if err := s.MarkDone("ghost", "x", "y"); err == nil {
		t.Fatal("unknown node must not be markable")
	}
}
