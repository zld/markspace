package dag

import (
	"errors"
	"sync"
	"testing"

	"mediasync/internal/clock"
)

func okNode(name string, deps ...string) *Node {
	return &Node{Name: name, Deps: deps, Params: map[string]string{"v": name},
		Run: func(n *Node, d map[string]*Node) (any, error) { return name + "-out", nil }}
}

func names(ns []*Node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Name
	}
	return out
}

func TestTopoAndIdempotentKeys(t *testing.T) {
	g, err := NewGraph([]*Node{
		okNode("index", "recover"),
		okNode("recover", "repair"),
		okNode("repair", "parse"),
		okNode("parse"),
	})
	if err != nil {
		t.Fatal(err)
	}
	order := g.Order()
	want := []string{"parse", "repair", "recover", "index"}
	for i, n := range order {
		if n.Name != want[i] {
			t.Fatalf("topo order=%v want %v", names(order), want)
		}
	}
	k1 := IdempotentKey(g.nodes["repair"], map[string]string{"parse": "k0"})
	k2 := IdempotentKey(g.nodes["repair"], map[string]string{"parse": "k0"})
	if k1 != k2 || len(k1) != 64 {
		t.Fatal("key must be stable sha256 hex")
	}
	k3 := IdempotentKey(g.nodes["repair"], map[string]string{"parse": "different"})
	if k1 == k3 {
		t.Fatal("key must change when dependency output changes")
	}
}

func TestCycleAndMissingDep(t *testing.T) {
	if _, err := NewGraph([]*Node{
		okNode("a", "b"),
		okNode("b", "a"),
	}); err == nil {
		t.Fatal("cycle must be rejected")
	}
	if _, err := NewGraph([]*Node{okNode("a", "ghost")}); err == nil {
		t.Fatal("missing dependency must be rejected")
	}
}

// memStore is an in-memory CheckpointStore for scheduler tests.
type memStore struct{ mu sync.Mutex; m map[string]any; writes int }

func (s *memStore) Load(k string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	return v, ok
}
func (s *memStore) Store(k string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.m[k]; !exists {
		s.writes++
	}
	s.m[k] = v // idempotent: same key, same value, no duplicate side effect
	return nil
}

func TestRetryLimitAndNoDuplicateWrites(t *testing.T) {
	attempts := 0
	flaky := &Node{Name: "flaky", Run: func(n *Node, d map[string]*Node) (any, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("transient io")
		}
		return "recovered", nil
	}}
	g, _ := NewGraph([]*Node{flaky})
	store := &memStore{m: map[string]any{}}
	sched, _ := NewScheduler(g, 2, store)
	res := sched.Run()
	if res.Blocked != nil || len(res.Failures) != 0 {
		t.Fatalf("flaky node should succeed on attempt 3: %+v", res.Failures)
	}
	if attempts != 3 || store.writes != 1 {
		t.Fatalf("attempts=%d writes=%d (want 3,1)", attempts, store.writes)
	}

	// Second run must reuse the checkpoint and never re-execute.
	sched2, _ := NewScheduler(g, 2, store)
	res2 := sched2.Run()
	if res2.Reused != 1 || attempts != 3 {
		t.Fatalf("checkpoint not reused: %+v", res2)
	}
}

func TestExhaustedRetriesReportBlockingChain(t *testing.T) {
	boom := &Node{Name: "parse", Run: func(n *Node, d map[string]*Node) (any, error) {
		return nil, errors.New("disk gone")
	}}
	g, _ := NewGraph([]*Node{
		okNode("index", "recover"),
		okNode("recover", "repair"),
		okNode("repair", "parse"),
		boom,
	})
	sched, _ := NewScheduler(g, 1, nil)
	res := sched.Run()
	if res.Blocked == nil {
		t.Fatal("expected a blocked dependency report")
	}
	if res.Blocked.BlockedBy != "parse" || res.Blocked.Node != "repair" {
		t.Fatalf("first blocked node wrong: %+v", res.Blocked)
	}
	chain := res.Blocked.Chain
	if chain[0] != "repair" || chain[len(chain)-1] != "parse" {
		t.Fatalf("dependency chain wrong: %v", chain)
	}
	if f := res.Failures["parse"]; f == nil || f.Attempts != 2 {
		t.Fatalf("failure record wrong: %+v", f)
	}
	// index must never run and must carry a structured dependency error.
	if !g.nodes["index"].Failed {
		t.Fatal("downstream node should be marked failed without executing")
	}
}

func TestInvalidArgumentNotRetried(t *testing.T) {
	calls := 0
	n := &Node{Name: "bad", Run: func(n *Node, d map[string]*Node) (any, error) {
		calls++
		return nil, clock.NewError(clock.ErrInvalidArgument, "bad param")
	}}
	g, _ := NewGraph([]*Node{n})
	sched, _ := NewScheduler(g, 5, nil)
	res := sched.Run()
	if calls != 1 || len(res.Failures) != 1 {
		t.Fatalf("invalid argument must fail fast, calls=%d", calls)
	}
}
