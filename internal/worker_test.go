package rtpreplay

import (
	"errors"
	"testing"
)

func simpleDag(t *testing.T, calls map[NodeID]int, failAlways map[NodeID]*Error) *Dag {
	t.Helper()
	mk := func(id NodeID, deps []NodeID, val string) *Node {
		return &Node{ID: id, Deps: deps, Run: func(map[NodeID]any) (any, error) {
			calls[id]++
			if err, ok := failAlways[id]; ok {
				return nil, err
			}
			return val, nil
		}}
	}
	d, err := NewDag([]*Node{
		mk("parse", nil, "parsed"),
		mk("metrics", []NodeID{"parse"}, "metrics"),
		mk("manifest", []NodeID{"parse", "metrics"}, "manifest"),
		mk("report", []NodeID{"manifest"}, "report"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestWorkerRunsInTopoOrder(t *testing.T) {
	var order []NodeID
	d, err := NewDag([]*Node{
		{ID: "c", Deps: []NodeID{"a", "b"}, Run: func(map[NodeID]any) (any, error) { order = append(order, "c"); return "c", nil }},
		{ID: "a", Run: func(map[NodeID]any) (any, error) { order = append(order, "a"); return "a", nil }},
		{ID: "b", Deps: []NodeID{"a"}, Run: func(map[NodeID]any) (any, error) { order = append(order, "b"); return "b", nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	w := NewWorker("cfg")
	res := w.Run(d)
	if !res.Completed || res.FirstBlocker != nil {
		t.Fatalf("res=%+v", res)
	}
	want := []NodeID{"a", "b", "c"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order=%v", order)
		}
	}
}

func TestWorkerRetriesRetryableOnly(t *testing.T) {
	attempts := 0
	d, _ := NewDag([]*Node{{
		ID: "flaky", MaxRetry: 3,
		Run: func(map[NodeID]any) (any, error) {
			attempts++
			if attempts < 3 {
				return nil, NewError(CodeInjected, "flaky", "transient").WithRetryable(true)
			}
			return "ok", nil
		},
	}})
	res := NewWorker("cfg").Run(d)
	if !res.Completed || res.Results[0].Attempts != 3 {
		t.Fatalf("res=%+v", res)
	}
}

func TestWorkerDoesNotRetryNonRetryable(t *testing.T) {
	attempts := 0
	d, _ := NewDag([]*Node{{
		ID: "fatal", MaxRetry: 5,
		Run: func(map[NodeID]any) (any, error) {
			attempts++
			return nil, NewError(CodeDuration, "fatal", "malformed")
		},
	}})
	res := NewWorker("cfg").Run(d)
	if res.Completed || attempts != 1 {
		t.Fatalf("attempts=%d res=%+v", attempts, res)
	}
	if res.Results[0].State != StateFailed {
		t.Fatalf("state=%s", res.Results[0].State)
	}
}

func TestWorkerBlocksDownstream(t *testing.T) {
	calls := map[NodeID]int{}
	d := simpleDag(t, calls, map[NodeID]*Error{"parse": NewError(CodeDuration, "parse", "bad")})
	res := NewWorker("cfg").Run(d)
	if res.Completed {
		t.Fatal("must not report completion")
	}
	if res.FirstBlocker == nil || res.FirstBlocker.Node != "parse" {
		t.Fatalf("blocker=%+v", res.FirstBlocker)
	}
	if calls["metrics"] != 0 || calls["manifest"] != 0 || calls["report"] != 0 {
		t.Fatalf("downstream ran: %v", calls)
	}
	states := map[NodeID]string{}
	for _, r := range res.Results {
		states[r.Node] = r.State
	}
	if states["metrics"] != StateBlocked || states["report"] != StateBlocked {
		t.Fatalf("states=%v", states)
	}
	for _, r := range res.Results {
		if r.State == StateBlocked && r.BlockedBy == "" {
			t.Fatalf("blocked node %s has no cause", r.Node)
		}
	}
}

func TestWorkerReportsFirstBlockedNodeNotLast(t *testing.T) {
	d, _ := NewDag([]*Node{
		{ID: "a", Run: func(map[NodeID]any) (any, error) { return nil, NewError(CodeInjected, "a", "bad") }},
		{ID: "b", Deps: []NodeID{"a"}, Run: func(map[NodeID]any) (any, error) { return "b", nil }},
	})
	res := NewWorker("cfg").Run(d)
	if res.FirstBlocker == nil || res.FirstBlocker.Node != "a" {
		t.Fatalf("blocker=%+v", res.FirstBlocker)
	}
}

func TestWorkerIdempotentCacheAndDigest(t *testing.T) {
	calls := map[NodeID]int{}
	d := simpleDag(t, calls, nil)
	w1 := NewWorker("cfg-v1")
	res1 := w1.Run(d)
	// A second worker seeded from the first run's artifacts must reuse them:
	// a fresh Worker has an empty in-memory cache, so this proves the key is
	// derived from input and configuration digests alone.
	w2 := NewWorker("cfg-v1")
	w2.SeedCache(res1.Artifacts)
	res2 := w2.Run(d)
	if !res1.Completed || !res2.Completed {
		t.Fatal("run failed")
	}
	if res1.ArtifactDigest != res2.ArtifactDigest {
		t.Fatalf("digest drift: %s vs %s", res1.ArtifactDigest, res2.ArtifactDigest)
	}
	if calls["parse"] != 1 {
		t.Fatalf("seeded run re-executed parse: %d", calls["parse"])
	}
	if calls["metrics"] != 1 || calls["manifest"] != 1 {
		t.Fatalf("seeded run re-executed downstream: %v", calls)
	}
	for _, a := range res2.Artifacts {
		if !a.Cached {
			t.Fatalf("artifact %s not marked cached", a.Node)
		}
	}
}

func TestWorkerDigestChangesWithInput(t *testing.T) {
	d1, _ := NewDag([]*Node{{ID: "x", Run: func(map[NodeID]any) (any, error) { return "one", nil }}})
	d2, _ := NewDag([]*Node{{ID: "x", Run: func(map[NodeID]any) (any, error) { return "two", nil }}})
	r1 := NewWorker("cfg").Run(d1)
	r2 := NewWorker("cfg").Run(d2)
	if r1.ArtifactDigest == r2.ArtifactDigest {
		t.Fatal("different outputs must produce different digests")
	}
}

func TestWorkerPanicBecomesFailure(t *testing.T) {
	d, _ := NewDag([]*Node{{ID: "boom", MaxRetry: 3, Run: func(map[NodeID]any) (any, error) { panic("oops") }}})
	res := NewWorker("cfg").Run(d)
	if res.Completed || res.Results[0].State != StateFailed {
		t.Fatalf("res=%+v", res)
	}
	if res.Results[0].Attempts != 1 {
		t.Fatalf("panic retried %d times", res.Results[0].Attempts)
	}
}

func TestNewDagRejectsInvalid(t *testing.T) {
	noop := func(map[NodeID]any) (any, error) { return nil, nil }
	cases := [][]*Node{
		{{ID: "", Run: noop}},
		{{ID: "a", Run: nil}},
		{{ID: "a", Run: noop}, {ID: "a", Run: noop}},
		{{ID: "a", Deps: []NodeID{"ghost"}, Run: noop}},
		{{ID: "a", Deps: []NodeID{"b"}, Run: noop}, {ID: "b", Deps: []NodeID{"a"}, Run: noop}},
	}
	for i, nodes := range cases {
		if _, err := NewDag(nodes); err == nil {
			t.Fatalf("case %d: expected config error", i)
		}
	}
}

func TestIsRetryableUntypedIsFalse(t *testing.T) {
	if IsRetryable(errors.New("plain")) {
		t.Fatal("untyped error must not be retryable")
	}
}
