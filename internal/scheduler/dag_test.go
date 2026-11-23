package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"archivecheck/internal/model"
)

// counter tracks executions and submissions per key.
type counter struct {
	mu          sync.Mutex
	executions  map[string]int
	submissions map[string]int
}

func newCounter() *counter {
	return &counter{executions: map[string]int{}, submissions: map[string]int{}}
}

func (c *counter) node(id string, deps []string, run func(context.Context, Key) (Artifact, error)) Node {
	c.mu.Lock()
	if c.executions == nil {
		c.executions = map[string]int{}
	}
	if c.submissions == nil {
		c.submissions = map[string]int{}
	}
	c.mu.Unlock()
	return Node{
		ID:           id,
		Deps:         deps,
		InputDigest:  "sha256:" + id + "-in",
		ParamsDigest: "p1",
		Run: func(ctx context.Context, key Key) (Artifact, error) {
			c.mu.Lock()
			c.executions[id]++
			c.submissions[key.Digest()]++
			c.mu.Unlock()
			return run(ctx, key)
		},
	}
}

func ok(detail string) func(context.Context, Key) (Artifact, error) {
	return func(context.Context, Key) (Artifact, error) {
		return Artifact{Digest: "d-" + detail, Size: int64(len(detail))}, nil
	}
}

func TestGraphTopologicalOrderDeterministic(t *testing.T) {
	var c counter
	g, err := NewGraph([]Node{
		c.node("encode", []string{"probe"}, ok("encode")),
		c.node("probe", nil, ok("probe")),
		c.node("archive", []string{"encode", "checksum"}, ok("archive")),
		c.node("checksum", []string{"probe"}, ok("checksum")),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"probe", "checksum", "encode", "archive"}
	got := g.Order()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order %v want %v", got, want)
	}
	// Rebuilding produces the same order.
	g2, _ := NewGraph([]Node{})
	_ = g2
	for i := 0; i < 5; i++ {
		gg, err := NewGraph([]Node{
			c.node("encode", []string{"probe"}, ok("encode")),
			c.node("probe", nil, ok("probe")),
			c.node("archive", []string{"encode", "checksum"}, ok("archive")),
			c.node("checksum", []string{"probe"}, ok("checksum")),
		})
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(gg.Order()) != fmt.Sprint(want) {
			t.Fatalf("nondeterministic order %v", gg.Order())
		}
	}
}

func TestGraphRejectsCycleAndUnknownDep(t *testing.T) {
	if _, err := NewGraph([]Node{
		{ID: "a", Deps: []string{"b"}},
		{ID: "b", Deps: []string{"a"}},
	}); !errors.Is(err, ErrCycle) {
		t.Fatalf("want cycle error, got %v", err)
	}
	if _, err := NewGraph([]Node{{ID: "a", Deps: []string{"missing"}}}); err == nil {
		t.Fatal("unknown dependency must fail")
	}
	if _, err := NewGraph([]Node{{ID: "a"}, {ID: "a"}}); err == nil {
		t.Fatal("duplicate node must fail")
	}
}

func TestRunSuccessAndOutputDigestStable(t *testing.T) {
	var c counter
	g, _ := NewGraph([]Node{
		c.node("a", nil, ok("aaa")),
		c.node("b", []string{"a"}, ok("bbb")),
		c.node("c", []string{"a"}, ok("ccc")),
	})
	rep, err := (&Runner{Concurrency: 4}).Run(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Succeeded() || rep.OutputDigest() == "" {
		t.Fatalf("report %+v", rep)
	}
	// Running twice yields the same output digest.
	rep2, _ := (&Runner{Concurrency: 4}).Run(context.Background(), g)
	if rep2.OutputDigest() != rep.OutputDigest() {
		t.Fatalf("digest changed: %s vs %s", rep.OutputDigest(), rep2.OutputDigest())
	}
}

func TestFailedDependencyBlocksDownstream(t *testing.T) {
	var c counter
	fail := func(context.Context, Key) (Artifact, error) {
		return Artifact{}, model.NewFault(model.CodeMP4BoxTruncated, model.SeverityError,
			model.Source{Layer: "inspect", Segment: "4", File: "v/4.m4s", Offset: 100}, "truncated")
	}
	g, _ := NewGraph([]Node{
		c.node("probe", nil, ok("probe")),
		c.node("inspect-4", []string{"probe"}, fail),
		c.node("inspect-5", []string{"probe"}, ok("inspect-5")),
		c.node("merge", []string{"inspect-4", "inspect-5"}, ok("merge")),
		c.node("report", []string{"merge"}, ok("report")),
	})
	rep, err := (&Runner{Concurrency: 2}).Run(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed != "inspect-4" {
		t.Fatalf("failed=%q", rep.Failed)
	}
	if rep.FirstBlocked != "merge" {
		t.Fatalf("first blocked=%q", rep.FirstBlocked)
	}
	if rep.FirstBlockedDependency != "inspect-4" {
		t.Fatalf("first blocked dep=%q", rep.FirstBlockedDependency)
	}
	if len(rep.Nodes) != 5 {
		t.Fatalf("nodes=%d", len(rep.Nodes))
	}
	for _, n := range rep.Nodes {
		if n.ID == "report" && n.Status != StatusBlocked {
			t.Fatalf("report should be blocked: %+v", n)
		}
		if n.ID == "inspect-4" && n.FaultCode != model.CodeMP4BoxTruncated {
			t.Fatalf("fault code %s", n.FaultCode)
		}
	}
	// The run counter must not have executed blocked nodes.
	if c.executions["merge"] != 0 || c.executions["report"] != 0 {
		t.Fatalf("blocked nodes ran: %+v", c.executions)
	}
}

func TestTransientRetryIsBoundedAndKeyed(t *testing.T) {
	var c counter
	var attempts atomic.Int32
	flaky := func(context.Context, Key) (Artifact, error) {
		if attempts.Add(1) < 3 {
			return Artifact{}, model.NewFault(model.CodeTransportShortRead, model.SeverityWarn,
				model.Source{Layer: "transport", Offset: -1}, "short read")
		}
		return Artifact{Digest: "final", Size: 5}, nil
	}
	n := c.node("fetch", nil, flaky)
	n.MaxAttempts = 4
	g, _ := NewGraph([]Node{n})
	rep, _ := (&Runner{Concurrency: 1}).Run(context.Background(), g)
	if rep.Nodes[0].Status != StatusOK {
		t.Fatalf("status=%s err=%s", rep.Nodes[0].Status, rep.Nodes[0].Error)
	}
	if rep.Nodes[0].Attempts != 3 || attempts.Load() != 3 {
		t.Fatalf("attempts=%d total=%d", rep.Nodes[0].Attempts, attempts.Load())
	}
	// All attempts used the same idempotency key, so no artifact was
	// submitted under two identities.
	if len(c.submissions) != 1 {
		t.Fatalf("submissions=%+v", c.submissions)
	}
}

func TestStructuralFailureIsNotRetried(t *testing.T) {
	var attempts atomic.Int32
	n := Node{
		ID: "inspect", InputDigest: "i", ParamsDigest: "p", MaxAttempts: 5,
		Run: func(context.Context, Key) (Artifact, error) {
			attempts.Add(1)
			return Artifact{}, model.NewFault(model.CodeMP4BoxTruncated, model.SeverityError,
				model.Source{Layer: "inspect", Offset: 12}, "bad")
		},
	}
	g, _ := NewGraph([]Node{n})
	rep, _ := (&Runner{}).Run(context.Background(), g)
	if attempts.Load() != 1 {
		t.Fatalf("structural failure retried %d times", attempts.Load())
	}
	if rep.Nodes[0].Status != StatusFailed {
		t.Fatalf("status %s", rep.Nodes[0].Status)
	}
}

func TestCompletedKeysAreReusedNotReexecuted(t *testing.T) {
	var c counter
	runCount := 0
	run := func(context.Context, Key) (Artifact, error) {
		runCount++
		return Artifact{Digest: "art", Size: 3}, nil
	}
	n := c.node("probe", nil, run)
	g, _ := NewGraph([]Node{n})
	first, _ := (&Runner{}).Run(context.Background(), g)
	if runCount != 1 || first.Nodes[0].Cached {
		t.Fatalf("first run executed=%d cached=%v", runCount, first.Nodes[0].Cached)
	}
	// Second run with the completed key recorded: no execution.
	completed := map[string]Artifact{first.Nodes[0].Key: first.Nodes[0].Artifact}
	second, _ := (&Runner{Completed: completed}).Run(context.Background(), g)
	if runCount != 1 {
		t.Fatalf("cached run re-executed: %d", runCount)
	}
	if !second.Nodes[0].Cached || second.OutputDigest() != first.OutputDigest() {
		t.Fatalf("cached result differs: %+v", second.Nodes[0])
	}
}

func TestChangedInputDoesNotReuseCompleted(t *testing.T) {
	executed := 0
	run := func(context.Context, Key) (Artifact, error) {
		executed++
		return Artifact{Digest: fmt.Sprintf("art-%d", executed), Size: 1}, nil
	}
	base := Node{ID: "probe", InputDigest: "in-1", ParamsDigest: "p", Run: run}
	g, _ := NewGraph([]Node{base})
	first, _ := (&Runner{}).Run(context.Background(), g)
	completed := map[string]Artifact{first.Nodes[0].Key: first.Nodes[0].Artifact}

	base.InputDigest = "in-2"
	g2, _ := NewGraph([]Node{base})
	second, _ := (&Runner{Completed: completed}).Run(context.Background(), g2)
	if executed != 2 {
		t.Fatalf("changed input should re-run, executed=%d", executed)
	}
	if second.Nodes[0].Cached {
		t.Fatal("changed input must not be served from cache")
	}
	if first.OutputDigest() == second.OutputDigest() {
		t.Fatal("different artifacts must yield different output digests")
	}
}

func TestConcurrentGraphExecution(t *testing.T) {
	const n = 24
	var nodes []Node
	started := make(chan struct{}, n)
	nodes = append(nodes, Node{ID: "root", InputDigest: "r", ParamsDigest: "p",
		Run: func(context.Context, Key) (Artifact, error) {
			return Artifact{Digest: "root", Size: 1}, nil
		}})
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		nodes = append(nodes, Node{
			ID: fmt.Sprintf("leaf-%02d", i), Deps: []string{"root"}, InputDigest: fmt.Sprintf("i%d", i), ParamsDigest: "p",
			Run: func(context.Context, Key) (Artifact, error) {
				defer wg.Done()
				started <- struct{}{}
				time.Sleep(time.Millisecond)
				return Artifact{Digest: fmt.Sprintf("leaf-%02d", i), Size: 1}, nil
			},
		})
	}
	g, err := NewGraph(nodes)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := (&Runner{Concurrency: 8}).Run(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if !rep.Succeeded() {
		t.Fatalf("report %+v", rep.Failed)
	}
	if len(rep.Order) != n+1 {
		t.Fatalf("order=%d", len(rep.Order))
	}
}

func TestOnCompleteFailureFailsNode(t *testing.T) {
	n := Node{ID: "probe", InputDigest: "i", ParamsDigest: "p",
		Run: func(context.Context, Key) (Artifact, error) {
			return Artifact{Digest: "a", Size: 1}, nil
		}}
	g, _ := NewGraph([]Node{n})
	rep, _ := (&Runner{OnComplete: func(Key, Artifact) error {
		return model.NewFault(model.CodeCheckpointCorrupt, model.SeverityError,
			model.Source{Layer: "checkpoint", Offset: -1}, "write failed")
	}}).Run(context.Background(), g)
	if rep.Nodes[0].Status != StatusFailed || rep.Nodes[0].FaultCode != model.CodeCheckpointCorrupt {
		t.Fatalf("node %+v", rep.Nodes[0])
	}
}

func TestKeyDigestIsStableAndDistinct(t *testing.T) {
	a := Key{NodeID: "n", InputDigest: "i", ParamsDigest: "p"}
	b := Key{NodeID: "n", InputDigest: "i", ParamsDigest: "p"}
	c := Key{NodeID: "n", InputDigest: "i2", ParamsDigest: "p"}
	if a.Digest() != b.Digest() {
		t.Fatal("same key must digest identically")
	}
	if a.Digest() == c.Digest() {
		t.Fatal("different inputs must digest differently")
	}
	if len(a.Digest()) != 64 {
		t.Fatalf("digest length %d", len(a.Digest()))
	}
}
