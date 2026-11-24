package dag

import (
	"context"
	"errors"
	"testing"
)

func TestSchedulerRunsDependenciesFirst(t *testing.T) {
	var order []string
	s, err := NewScheduler(
		Node{Name: "manifest", Dependencies: []string{"align", "metadata"}, Run: func(context.Context) error {
			order = append(order, "manifest")
			return nil
		}},
		Node{Name: "align", Dependencies: []string{"scan"}, Run: func(context.Context) error {
			order = append(order, "align")
			return nil
		}},
		Node{Name: "metadata", Run: func(context.Context) error {
			order = append(order, "metadata")
			return nil
		}},
		Node{Name: "scan", Run: func(context.Context) error {
			order = append(order, "scan")
			return nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	rep := s.Run(context.Background())
	if !rep.Successful() {
		t.Fatalf("run failed: %+v", rep.Results)
	}
	index := map[string]int{}
	for i, n := range order {
		index[n] = i
	}
	if index["scan"] >= index["align"] || index["align"] >= index["manifest"] {
		t.Fatalf("dependency order wrong: %v", order)
	}
	if index["metadata"] >= index["manifest"] {
		t.Fatalf("metadata must precede manifest: %v", order)
	}
}

func TestExhaustedNodeBlocksDependentsAndReportsDependency(t *testing.T) {
	boom := errors.New("write failed")
	attempts := 0
	s, err := NewScheduler(
		Node{Name: "writer", MaxAttempts: 2, Run: func(context.Context) error {
			attempts++
			return boom
		}},
		Node{Name: "manifest", Dependencies: []string{"writer"}, Run: func(context.Context) error {
			t.Fatal("manifest must not run after failed writer")
			return nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	rep := s.Run(context.Background())
	if attempts != 2 {
		t.Fatalf("attempts=%d want 2", attempts)
	}
	if rep.Results["writer"].Status != StatusFailed {
		t.Fatalf("writer=%s want failed", rep.Results["writer"].Status)
	}
	if rep.Results["manifest"].Status != StatusSkipped {
		t.Fatalf("manifest=%s want skipped", rep.Results["manifest"].Status)
	}
	if rep.FirstFailedNode != "writer" || rep.FirstBlockedDependency != "writer" {
		t.Fatalf("blocking report failed=%q blocked=%q node=%q",
			rep.FirstFailedNode, rep.FirstBlockedDependency, rep.FirstBlockedNode)
	}
}

func TestCancelStopsLaterNodesAndPreservesCompleted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan string, 4)
	s, err := NewScheduler(
		Node{Name: "a", Run: func(context.Context) error {
			ran <- "a"
			cancel()
			return nil
		}},
		Node{Name: "b", Run: func(context.Context) error {
			ran <- "b"
			return nil
		}},
		Node{Name: "c", Dependencies: []string{"b"}, Run: func(context.Context) error {
			ran <- "c"
			return nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	rep := s.Run(ctx)
	if rep.Results["a"].Status != StatusSuccess {
		t.Fatalf("a=%s want success", rep.Results["a"].Status)
	}
	if rep.Results["b"].Status != StatusCanceled || rep.Results["c"].Status != StatusCanceled {
		t.Fatalf("b=%s c=%s want canceled", rep.Results["b"].Status, rep.Results["c"].Status)
	}
	if !rep.Canceled {
		t.Fatal("report.Canceled=false want true")
	}
	close(ran)
	for n := range ran {
		if n != "a" {
			t.Fatalf("later node %q ran after cancellation", n)
		}
	}
}

func TestCycleRejected(t *testing.T) {
	_, err := NewScheduler(
		Node{Name: "a", Dependencies: []string{"b"}, Run: func(context.Context) error { return nil }},
		Node{Name: "b", Dependencies: []string{"a"}, Run: func(context.Context) error { return nil }},
	)
	if err == nil {
		t.Fatal("expected cycle validation error")
	}
}

func TestIdempotentRetryDoesNotDuplicateCommit(t *testing.T) {
	store := NewIdempotentStore()
	committedAmbiguous := false
	attempts, err := Retry(context.Background(), store, "manifest", Policy{MaxAttempts: 3}, func(int) error {
		if !committedAmbiguous {
			if _, err := store.Commit("manifest", []byte("same")); err != nil {
				return err
			}
			committedAmbiguous = true
			return errors.New("commit acknowledgement lost")
		}
		t.Fatal("retry callback should not run after known commit")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.Commits() != 1 {
		t.Fatalf("commits=%d want 1", store.Commits())
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d want one discovery step", attempts)
	}

	failures := 0
	attempts, err = Retry(context.Background(), store, "metrics", Policy{MaxAttempts: 3}, func(int) error {
		failures++
		if failures < 3 {
			return errors.New("temporary")
		}
		_, err := store.Commit("metrics", []byte("m"))
		return err
	})
	if err != nil || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	if store.Commits() != 2 {
		t.Fatalf("total commits=%d want 2", store.Commits())
	}
}
