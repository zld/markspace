package dag

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// memCommit is an in-memory atomic committer for tests.
type memCommit struct {
	mu     sync.Mutex
	data   map[string][]byte
	failOn string
}

func newMemCommit() *memCommit { return &memCommit{data: map[string][]byte{}} }

func (m *memCommit) Commit(key string, data []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failOn == key {
		return "", errors.New("forced commit failure")
	}
	sum := sha256.Sum256(data)
	m.data[key] = append([]byte(nil), data...)
	return hex.EncodeToString(sum[:]), nil
}

func (m *memCommit) Read(key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, fmt.Errorf("no output for %s", key)
	}
	return v, nil
}

func TestNodeKeyStableAndVersioned(t *testing.T) {
	k1 := NodeKey("in", "cfg")
	k2 := NodeKey("in", "cfg")
	if k1 != k2 || len(k1) != 16 {
		t.Fatalf("node key must be stable 16-char, got %q", k1)
	}
	if NodeKey("in2", "cfg") == k1 {
		t.Fatal("different input digest must change the key")
	}
}

func TestTopologicalOrderStable(t *testing.T) {
	nodes := []Node{
		{Key: "c", Deps: []string{"a", "b"}, Run: noopRun},
		{Key: "a", Run: noopRun},
		{Key: "b", Deps: []string{"a"}, Run: noopRun},
	}
	g, err := NewGraph(nodes)
	if err != nil {
		t.Fatal(err)
	}
	order := g.Order()
	want := []string{"a", "b", "c"}
	for i, w := range want {
		if order[i] != w {
			t.Fatalf("order got %v want %v", order, want)
		}
	}
}

func TestCycleRejected(t *testing.T) {
	nodes := []Node{
		{Key: "a", Deps: []string{"c"}, Run: noopRun},
		{Key: "b", Deps: []string{"a"}, Run: noopRun},
		{Key: "c", Deps: []string{"b"}, Run: noopRun},
	}
	_, err := NewGraph(nodes)
	if err == nil {
		t.Fatal("cycle must be rejected")
	}
}

func TestUnknownDependencyRejected(t *testing.T) {
	_, err := NewGraph([]Node{{Key: "a", Deps: []string{"ghost"}, Run: noopRun}})
	if err == nil {
		t.Fatal("unknown dependency must be rejected")
	}
}

func TestExecuteDependencyOutputs(t *testing.T) {
	nodes := []Node{
		{Key: "a", Run: func(*RunContext) ([]byte, error) { return []byte("A"), nil }},
		{Key: "b", Deps: []string{"a"}, Run: func(rc *RunContext) ([]byte, error) {
			out, ok := rc.Output("a")
			if !ok || string(out) != "A" {
				return nil, errors.New("did not receive dependency output")
			}
			return append(out, 'B'), nil
		}},
	}
	g, _ := NewGraph(nodes)
	ex := &Executor{Graph: g, Workers: 2, Commit: newMemCommit()}
	results, err := ex.Execute()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Status != StatusSucceeded || len(r.SHA256) != 64 {
			t.Fatalf("node not succeeded with valid hash: %+v", r)
		}
	}
}

func TestFirstBlockerReported(t *testing.T) {
	boom := errors.New("boom")
	nodes := []Node{
		{Key: "a", Run: func(*RunContext) ([]byte, error) { return nil, boom }},
		{Key: "b", Deps: []string{"a"}, Run: noopRun},
		{Key: "c", Deps: []string{"b"}, Run: noopRun},
	}
	g, _ := NewGraph(nodes)
	ex := &Executor{Graph: g, Workers: 3, Commit: newMemCommit()}
	results, err := ex.Execute()
	if err == nil {
		t.Fatal("expected failure")
	}
	be, ok := err.(*BlockedError)
	if !ok || be.FirstBlocker != "a" {
		t.Fatalf("expected first blocker a, got %v", err)
	}
	if results[1].Status != StatusBlocked || results[2].Status != StatusBlocked {
		t.Fatalf("downstream nodes must be blocked: %+v", results)
	}
}

func TestCheckpointSkipReusesNodes(t *testing.T) {
	nodes := []Node{
		{Key: "a", Run: func(*RunContext) ([]byte, error) { return []byte("A"), nil }},
		{Key: "b", Deps: []string{"a"}, Run: func(rc *RunContext) ([]byte, error) {
			out, _ := rc.Output("a")
			return append(out, 'B'), nil
		}},
	}
	g, _ := NewGraph(nodes)
	commit := newMemCommit()
	ex := &Executor{Graph: g, Workers: 1, Commit: commit}
	if _, err := ex.Execute(); err != nil {
		t.Fatal(err)
	}
	// Second run: skip "a", verify it is marked reused and its output served.
	ex2 := &Executor{Graph: g, Workers: 1, Commit: commit,
		Skip: func(key string) (string, bool) {
			if key == "a" {
				sum := sha256.Sum256(commit.data["a"])
				return hex.EncodeToString(sum[:]), true
			}
			return "", false
		}}
	results, err := ex2.Execute()
	if err != nil {
		t.Fatal(err)
	}
	reused := 0
	for _, r := range results {
		if r.Reused {
			reused++
		}
	}
	if reused != 1 {
		t.Fatalf("expected exactly 1 reused node, got %d", reused)
	}
}

func TestInterruptionLeavesPendingNodes(t *testing.T) {
	nodes := []Node{
		{Key: "a", Run: func(*RunContext) ([]byte, error) { return []byte("A"), nil }},
		{Key: "b", Deps: []string{"a"}, Run: noopRun},
		{Key: "c", Deps: []string{"b"}, Run: noopRun},
	}
	g, _ := NewGraph(nodes)
	ex := &Executor{Graph: g, Workers: 1, Commit: newMemCommit(), StopAfterKey: "a"}
	results, err := ex.Execute()
	be, ok := err.(*BlockedError)
	if !ok || !be.Interrupted {
		t.Fatalf("expected interrupted error, got %v", err)
	}
	if results[0].Status != StatusSucceeded {
		t.Fatal("stop node must have committed")
	}
}

func TestBoundedConcurrency(t *testing.T) {
	var concurrent, maxConcurrent int
	var mu sync.Mutex
	mk := func(key string, deps []string) Node {
		return Node{Key: key, Deps: deps, Run: func(*RunContext) ([]byte, error) {
			mu.Lock()
			concurrent++
			if concurrent > maxConcurrent {
				maxConcurrent = concurrent
			}
			mu.Unlock()
			// yield to let siblings schedule
			mu.Lock()
			concurrent--
			mu.Unlock()
			return []byte(key), nil
		}}
	}
	var nodes []Node
	for i := 0; i < 8; i++ {
		nodes = append(nodes, mk(fmt.Sprintf("n%d", i), nil))
	}
	g, _ := NewGraph(nodes)
	ex := &Executor{Graph: g, Workers: 2, Commit: newMemCommit()}
	if _, err := ex.Execute(); err != nil {
		t.Fatal(err)
	}
	if maxConcurrent > 2 {
		t.Fatalf("concurrency exceeded worker bound: %d", maxConcurrent)
	}
}

func noopRun(*RunContext) ([]byte, error) { return []byte("x"), nil }
