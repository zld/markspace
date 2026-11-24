package dag

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func node(name string, deps ...string) Node {
	return Node{
		Name: name, Kind: "test",
		Params:    map[string]string{"name": name},
		DependsOn: deps,
		Inputs:    []string{"input-a"},
		Run: func(ctx context.Context, key string, d map[string]Result) (Result, error) {
			return Result{Hash: "h-" + name}, nil
		},
	}
}

func TestKeyIsStableAndContentAddressed(t *testing.T) {
	n := node("a")
	k1 := KeyOf(&n, map[string]Result{})
	k2 := KeyOf(&n, map[string]Result{})
	if k1 != k2 {
		t.Fatalf("key not stable: %s vs %s", k1, k2)
	}
	// Changing a parameter changes the key.
	n2 := node("a")
	n2.Params["name"] = "b"
	if KeyOf(&n2, nil) == k1 {
		t.Fatal("parameter change did not change the key")
	}
	// Changing a dependency output changes the key.
	withDep := node("a", "dep")
	if KeyOf(&withDep, map[string]Result{"dep": {Hash: "x"}}) == KeyOf(&withDep, map[string]Result{"dep": {Hash: "y"}}) {
		t.Fatal("dependency hash change did not change the key")
	}
	if KeyOf(&withDep, map[string]Result{"dep": {Hash: "x"}}) == KeyOf(&n2, nil) {
		t.Fatal("different graphs produced the same key")
	}
}

func TestBuildRejectsCycleAndUnknownEdges(t *testing.T) {
	if _, err := Build([]Node{node("a", "b"), node("b", "a")}); err == nil {
		t.Fatal("cycle accepted")
	}
	if _, err := Build([]Node{node("a", "missing")}); err == nil {
		t.Fatal("unknown dependency accepted")
	}
	if _, err := Build([]Node{node("a"), node("a")}); err == nil {
		t.Fatal("duplicate node accepted")
	}
}

func TestIndependentBranchesBothComplete(t *testing.T) {
	g, err := Build([]Node{node("root"), node("left", "root"), node("right", "root")})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := (&Runner{Workers: 1}).Run(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range rep.States {
		if s.Status != StatusDone {
			t.Errorf("node %s status %s", s.Name, s.Status)
		}
	}
}

func TestFailureStopsDescendantsButNotSiblings(t *testing.T) {
	bad := node("bad")
	bad.Run = func(ctx context.Context, key string, d map[string]Result) (Result, error) {
		return Result{}, &Failure{Node: "bad", Stage: "test", Offset: 188, Packet: 1,
			Detail: "located failure", Err: errors.New("boom")}
	}
	good := node("good")
	var ranChi int
	child := node("child", "bad")
	child.Run = func(ctx context.Context, key string, d map[string]Result) (Result, error) {
		ranChi++
		return Result{Hash: "h-child"}, nil
	}
	g, err := Build([]Node{bad, good, child})
	if err != nil {
		t.Fatal(err)
	}
	rep, _ := (&Runner{Workers: 1}).Run(context.Background(), g)
	st, _ := rep.State("good")
	if st.Status != StatusDone {
		t.Errorf("independent branch did not finish: %+v", st)
	}
	st, _ = rep.State("child")
	if st.Status != StatusBlocked || st.BlockedBy != "bad" {
		t.Errorf("descendant not blocked by bad: %+v", st)
	}
	if ranChi != 0 {
		t.Fatalf("blocked descendant ran %d times", ranChi)
	}
	if rep.BlockedBy() != "bad" {
		t.Errorf("BlockedBy=%q want bad", rep.BlockedBy())
	}
	f, ok := rep.Failed()
	if !ok || f.Name != "bad" {
		t.Fatalf("failed node not reported: %+v", f)
	}
	var fe *Failure
	if !errors.As(f.Err, &fe) || fe.Offset != 188 {
		t.Fatalf("failure location lost: %v", f.Err)
	}
}

func TestIdempotentRerunFromCache(t *testing.T) {
	cache := NewMemoryCache()
	n := node("a")
	n.AllowRecovery = true
	var runs int
	n.Run = func(ctx context.Context, key string, d map[string]Result) (Result, error) {
		runs++
		return Result{Hash: "h-a"}, nil
	}
	g, err := Build([]Node{n})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Runner{Workers: 1, Cache: cache, AllowRecovery: true}).Run(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	calls := runs
	rep, err := (&Runner{Workers: 1, Cache: cache, AllowRecovery: true}).Run(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	if runs != calls {
		t.Fatalf("cached node recomputed: runs %d -> %d", calls, runs)
	}
	if rep.Recovered() != 1 {
		t.Fatalf("recovered=%d want 1", rep.Recovered())
	}
	if rep.States[0].Status != StatusRecovered {
		t.Fatalf("state %s", rep.States[0].Status)
	}
}

func TestRetryIsTransientOnly(t *testing.T) {
	var attempts int
	err := Retry(context.Background(), Policy{Attempts: 5, Backoff: time.Microsecond},
		func(ctx context.Context, a int) error {
			attempts++
			return errors.New("transient")
		})
	if err == nil {
		t.Fatal("retry returned nil for a failing function")
	}
	if attempts != 5 {
		t.Errorf("attempts=%d want 5", attempts)
	}
	attempts = 0
	err = Retry(context.Background(), Policy{Attempts: 5}, func(ctx context.Context, a int) error {
		attempts++
		return &Permanent{Err: errors.New("fatal")}
	})
	if attempts != 1 {
		t.Errorf("permanent error retried %d times", attempts)
	}
}

func TestRetryStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Retry(ctx, Policy{Attempts: 3}, func(c context.Context, a int) error {
		return errors.New("transient")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
}

func TestCommitIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	committed := 0
	n := node("a")
	n.Commit = func(ctx context.Context, key string, r Result) error {
		committed++
		return os.WriteFile(filepath.Join(dir, "artifact"), []byte(r.Hash), 0o644)
	}
	g, err := Build([]Node{n})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Runner{Workers: 1}).Run(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "artifact"))
	if string(b) != "h-a" {
		t.Fatalf("committed artifact %q", b)
	}
	// A second run commits again but must not change the artifact: the key
	// is identical and the content is unchanged.
	if _, err := (&Runner{Workers: 1}).Run(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(filepath.Join(dir, "artifact"))
	if string(b2) != string(b) {
		t.Fatalf("committed artifact changed across reruns: %q -> %q", b, b2)
	}
}

func TestDiskCacheRejectsTampering(t *testing.T) {
	dir := t.TempDir()
	c, err := NewDiskCache(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Put("k1", Result{Hash: "h"}); err != nil {
		t.Fatal(err)
	}
	if r, ok := c.Get("k1"); !ok || r.Hash != "h" {
		t.Fatalf("roundtrip: %+v ok=%v", r, ok)
	}
	path := filepath.Join(dir, "k1.json")
	if err := os.WriteFile(path, []byte(`{"key":"k1","hash":"tampered"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("k1"); ok {
		t.Fatal("tampered cache entry accepted")
	}
}

func TestParallelSchedulerMatchesSequential(t *testing.T) {
	names := []string{"root", "a", "b", "c", "d", "e"}
	edges := map[string][]string{
		"root": nil, "a": {"root"}, "b": {"root"}, "c": {"a"}, "d": {"b"}, "e": {"c"},
	}
	build := func() *Graph {
		var nodes []Node
		for _, n := range names {
			nodes = append(nodes, node(n, edges[n]...))
		}
		g, err := Build(nodes)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		return g
	}
	seq, err := (&Runner{Workers: 1}).Run(context.Background(), build())
	if err != nil {
		t.Fatal(err)
	}
	par, err := (&Runner{Workers: 4}).Run(context.Background(), build())
	if err != nil {
		t.Fatal(err)
	}
	if len(seq.States) != len(par.States) {
		t.Fatalf("state count %d != %d", len(seq.States), len(par.States))
	}
	for i := range seq.States {
		if seq.States[i].Name != par.States[i].Name || seq.States[i].Status != par.States[i].Status {
			t.Errorf("state %d: seq=%+v par=%+v", i, seq.States[i], par.States[i])
		}
	}
}

func TestConcurrentCommitsUseDistinctKeys(t *testing.T) {
	cache := NewMemoryCache()
	var mu sync.Mutex
	seen := map[string]int{}
	var nodes []Node
	for i := 0; i < 16; i++ {
		i := i
		n := node(fmt.Sprintf("n%02d", i))
		n.Run = func(ctx context.Context, key string, d map[string]Result) (Result, error) {
			mu.Lock()
			seen[key]++
			mu.Unlock()
			return Result{Hash: fmt.Sprintf("h%d", i)}, nil
		}
		nodes = append(nodes, n)
	}
	g, err := Build(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Runner{Workers: 8, Cache: cache}).Run(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 16 {
		t.Fatalf("16 distinct keys expected, got %d", len(seen))
	}
}
