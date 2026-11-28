package dag

import (
	"errors"
	"path/filepath"
	"testing"

	"mediakernel/internal/fault"
)

func TestRetryIdempotent(t *testing.T) {
	calls, commits := 0, 0
	err := Do(3, func() error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		commits++ // commit happens only on the succeeding attempt
		return nil
	})
	if err != nil || calls != 3 || commits != 1 {
		t.Fatalf("calls=%d commits=%d err=%v", calls, commits, err)
	}
	if err := Do(2, func() error { return errors.New("always") }); err == nil {
		t.Fatal("expected failure")
	}
}

func TestSchedulerOrderAndReuse(t *testing.T) {
	dir := t.TempDir()
	build := func(runs *map[string]int) *Scheduler {
		s, err := NewScheduler(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"a", "b", "c"} {
			id := id
			var deps []string
			if id == "b" {
				deps = []string{"a"}
			}
			if id == "c" {
				deps = []string{"b"}
			}
			s.Add(&Node{ID: id, Deps: deps, Config: "v1", Run: func() ([]byte, error) {
				(*runs)[id]++
				return []byte("out-" + id), nil
			}})
		}
		return s
	}
	runs := map[string]int{}
	s := build(&runs)
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	if runs["a"] != 1 || runs["b"] != 1 || runs["c"] != 1 {
		t.Fatalf("runs=%v", runs)
	}
	// Second run reuses every node from the checkpoint.
	s2 := build(&runs)
	if err := s2.Run(); err != nil {
		t.Fatal(err)
	}
	if s2.Reused != 3 || runs["a"] != 1 {
		t.Fatalf("reused=%d runs=%v", s2.Reused, runs)
	}
}

func TestSchedulerFailureReportsBlockingNode(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewScheduler(dir)
	s.Add(&Node{ID: "ingest", Run: func() ([]byte, error) {
		return nil, fault.New(fault.ErrMissingSegment, 42, "gap")
	}})
	s.Add(&Node{ID: "scan", Deps: []string{"ingest"}, Run: func() ([]byte, error) {
		return []byte("x"), nil
	}})
	err := s.Run()
	if !fault.Is(err, fault.ErrDependency) {
		t.Fatalf("err=%v", err)
	}
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Node != "ingest" {
		t.Fatalf("blocking node: %v", err)
	}
	if s.StatusOf("scan") != Pending {
		t.Fatal("downstream must stay pending")
	}
}

func TestSchedulerInterruptResume(t *testing.T) {
	dir := t.TempDir()
	counts := map[string]int{}
	build := func(interrupt int) *Scheduler {
		s, _ := NewScheduler(dir)
		s.InterruptAfter = interrupt
		for _, id := range []string{"n1", "n2", "n3", "n4"} {
			id := id
			s.Add(&Node{ID: id, Run: func() ([]byte, error) {
				counts[id]++
				return []byte(id), nil
			}})
		}
		return s
	}
	if err := build(2).Run(); err != ErrInterrupted {
		t.Fatalf("interrupt: %v", err)
	}
	s := build(0)
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	if s.Reused != 2 {
		t.Fatalf("reused=%d want 2", s.Reused)
	}
	for id, c := range counts {
		if c != 1 {
			t.Fatalf("%s ran %d times", id, c)
		}
	}
	if _, err := filepath.Abs(filepath.Join(dir, "out", "n4.bin")); err != nil {
		t.Fatal(err)
	}
}
