package dag

import (
	"errors"
	"sync"
	"testing"

	"archivist/internal/model"
)

func linearGraph() []*Node {
	return []*Node{
		{ID: "parse", Stage: model.StageContainer, InputDigest: "in", ParamDigest: "p"},
		{ID: "index", Stage: model.StageIndex, Deps: []string{"parse"}, InputDigest: "in", ParamDigest: "p"},
		{ID: "align", Stage: model.StageTimeline, Deps: []string{"index"}, InputDigest: "in", ParamDigest: "p"},
		{ID: "manifest", Stage: model.StageManifest, Deps: []string{"align"}, InputDigest: "in", ParamDigest: "p"},
	}
}

func TestTopologicalOrderAndCycle(t *testing.T) {
	g, err := NewGraph(linearGraph())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"parse", "index", "align", "manifest"}
	for i, id := range g.Order() {
		if id != want[i] {
			t.Fatalf("order = %v", g.Order())
		}
	}
	cyclic := []*Node{
		{ID: "a", Deps: []string{"b"}},
		{ID: "b", Deps: []string{"a"}},
	}
	if _, err := NewGraph(cyclic); err == nil {
		t.Fatal("expected cycle rejection")
	}
}

func TestTransientRetryThenSuccess(t *testing.T) {
	nodes := linearGraph()
	var mu sync.Mutex
	failures := 2
	nodes[1].Execute = func(*RunContext) error {
		mu.Lock()
		defer mu.Unlock()
		if failures > 0 {
			failures--
			return errors.New("transient IO stall")
		}
		return nil
	}
	for _, n := range nodes {
		if n.Execute == nil {
			n.Execute = func(*RunContext) error { return nil }
		}
	}
	g, _ := NewGraph(nodes)
	r := NewRunner(g, Config{MaxRetries: 2})
	rep, _ := r.Execute()
	if rep.FirstBlocker != nil {
		t.Fatalf("unexpected blocker: %v", rep.Failed())
	}
	if rep.Results["index"].Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", rep.Results["index"].Attempts)
	}
	if r.CommitCount() != 4 {
		t.Fatalf("committed = %d, want 4", r.CommitCount())
	}
}

func TestPermanentFailureBlocksDownstream(t *testing.T) {
	nodes := linearGraph()
	for _, n := range nodes {
		n.Execute = func(*RunContext) error { return nil }
	}
	nodes[1].Permanent = true
	nodes[1].Execute = func(*RunContext) error {
		return model.AtTrack(model.StageIndex, model.ErrPermanentFailure, 2, "corrupt sample table")
	}
	g, _ := NewGraph(nodes)
	r := NewRunner(g, Config{MaxRetries: 3})
	rep, _ := r.Execute()
	if rep.FirstBlocker == nil || rep.FirstBlocker.NodeID != "index" {
		t.Fatalf("first blocker wrong: %+v", rep.FirstBlocker)
	}
	if rep.Results["index"].Attempts != 1 {
		t.Fatal("permanent failure must not be retried")
	}
	for _, id := range []string{"align", "manifest"} {
		if rep.Results[id].State != StateBlocked {
			t.Fatalf("%s should be blocked, got %s", id, rep.Results[id].State)
		}
	}
	down := rep.DownstreamUnrun(g)
	if len(down) != 2 {
		t.Fatalf("downstream unrun = %v", down)
	}
	blocker, err := r.Blocker("manifest")
	if err != nil || blocker == nil || blocker.NodeID != "index" {
		t.Fatalf("blocker resolution wrong: %+v %v", blocker, err)
	}
}

func TestIdempotentRerunNoDoubleCommit(t *testing.T) {
	nodes := linearGraph()
	execCount := map[string]int{}
	var mu sync.Mutex
	for _, n := range nodes {
		id := n.ID
		n.Execute = func(*RunContext) error {
			mu.Lock()
			execCount[id]++
			mu.Unlock()
			return nil
		}
	}
	g, _ := NewGraph(nodes)
	r1 := NewRunner(g, Config{MaxRetries: 0})
	rep1, _ := r1.Execute()
	if rep1.Committed != 4 {
		t.Fatalf("first run committed %d", rep1.Committed)
	}
	// Second runner restores the ledger; every node must short-circuit.
	r2 := NewRunner(g, Config{MaxRetries: 0})
	snap := Snapshot{Committed: map[string]CommittedArtifact{}}
	for _, art := range collectArtifacts(r1) {
		snap.Committed[art.IdempotencyKey] = art
	}
	restored := r2.Restore(&snap)
	if restored != 4 {
		t.Fatalf("restored = %d", restored)
	}
	rep2, _ := r2.Execute()
	if rep2.Committed != 4 {
		t.Fatalf("rerun committed count = %d", rep2.Committed)
	}
	for id, c := range execCount {
		if c != 1 {
			t.Fatalf("node %s executed %d times across rerun", id, c)
		}
	}
}

func TestIdempotencyKeyChangesWithInput(t *testing.T) {
	a := &Node{ID: "x", InputDigest: "in-a", ParamDigest: "p"}
	b := &Node{ID: "x", InputDigest: "in-b", ParamDigest: "p"}
	if a.IdempotencyKey() == b.IdempotencyKey() {
		t.Fatal("different input digests must yield different keys")
	}
	c := &Node{ID: "x", InputDigest: "in-a", ParamDigest: "p"}
	if a.IdempotencyKey() != c.IdempotencyKey() {
		t.Fatal("same inputs must yield the same key")
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	nodes := linearGraph()
	for _, n := range nodes {
		n.Execute = func(*RunContext) error { return nil }
	}
	g, _ := NewGraph(nodes)
	r := NewRunner(g, Config{})
	if _, err := r.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SaveSnapshot(dir); err != nil {
		t.Fatal(err)
	}
	snap, ok, err := LoadSnapshot(dir)
	if err != nil || !ok {
		t.Fatalf("snapshot load: ok=%v err=%v", ok, err)
	}
	if len(snap.Committed) != 4 {
		t.Fatalf("snapshot committed = %d", len(snap.Committed))
	}
	r2 := NewRunner(g, Config{})
	if got := r2.Restore(snap); got != 4 {
		t.Fatalf("restore count = %d", got)
	}
}

func TestRerunnableExcludesCommitted(t *testing.T) {
	nodes := linearGraph()
	for _, n := range nodes {
		n.Execute = func(*RunContext) error { return nil }
	}
	g, _ := NewGraph(nodes)
	r := NewRunner(g, Config{})
	if _, err := r.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(r.Rerunnable()) != 0 {
		t.Fatalf("all nodes committed, rerunnable should be empty: %v", r.Rerunnable())
	}
}

func collectArtifacts(r *Run) []CommittedArtifact {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]CommittedArtifact, 0, len(r.committed))
	for _, a := range r.committed {
		out = append(out, a)
	}
	return out
}
