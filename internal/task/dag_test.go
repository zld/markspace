package task

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
)

func decodeString(raw json.RawMessage) (any, error) {
	var s string
	err := json.Unmarshal(raw, &s)
	return s, err
}

func mkNode(name string, deps []string, input string, run func() (any, error)) *Node {
	return &Node{
		Name:    name,
		Deps:    deps,
		Version: "v1",
		Input:   []byte(input),
		Run:     func(map[string]any) (any, error) { return run() },
		Decode:  decodeString,
	}
}

func TestResumeReusesDoneNodes(t *testing.T) {
	ckptPath := filepath.Join(t.TempDir(), "ckpt.json")
	runs := map[string]int{}
	build := func() *Scheduler {
		ckpt, err := LoadCheckpoint(ckptPath)
		if err != nil {
			t.Fatal(err)
		}
		nodes := []*Node{
			mkNode("a", nil, "in-a", func() (any, error) { runs["a"]++; return "ra", nil }),
			mkNode("b", []string{"a"}, "in-b", func() (any, error) { runs["b"]++; return "rb", nil }),
			mkNode("c", []string{"b"}, "in-c", func() (any, error) { runs["c"]++; return "rc", nil }),
		}
		s, err := NewScheduler(nodes, ckpt)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	rep1 := build().Run()
	if rep1.FirstBlocked != "" || rep1.Executed != 3 {
		t.Fatalf("first run: executed=%d blocked=%q", rep1.Executed, rep1.FirstBlocked)
	}
	d1 := rep1.States["c"].Digest

	rep2 := build().Run()
	if rep2.Reused != 3 {
		t.Fatalf("second run reused = %d, want 3", rep2.Reused)
	}
	if rep2.States["c"].Digest != d1 {
		t.Fatal("digest changed across resume; results not idempotent")
	}
	if runs["a"] != 1 || runs["b"] != 1 || runs["c"] != 1 {
		t.Fatalf("nodes re-executed: %v", runs)
	}
}

func TestFirstBlockedDependencyAndRecovery(t *testing.T) {
	ckptPath := filepath.Join(t.TempDir(), "ckpt.json")
	failB := true
	build := func() *Scheduler {
		ckpt, _ := LoadCheckpoint(ckptPath)
		nodes := []*Node{
			mkNode("a", nil, "in-a", func() (any, error) { return "ra", nil }),
			mkNode("b", []string{"a"}, "in-b", func() (any, error) {
				if failB {
					return nil, &Error{Category: CatNoAnchor, Msg: "no RTCP anchor"}
				}
				return "rb", nil
			}),
			mkNode("c", []string{"b"}, "in-c", func() (any, error) { return "rc", nil }),
		}
		s, err := NewScheduler(nodes, ckpt)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	rep1 := build().Run()
	if rep1.FirstBlocked != "b" || rep1.BlockedCategory != CatNoAnchor {
		t.Fatalf("first blocked = %q (%s), want b (no-anchor)",
			rep1.FirstBlocked, rep1.BlockedCategory)
	}
	if rep1.States["c"].Status != "blocked" {
		t.Fatalf("c status = %s, want blocked", rep1.States["c"].Status)
	}
	if rep1.States["b"].Retries != 1 {
		t.Fatalf("b retries = %d, want 1", rep1.States["b"].Retries)
	}

	// fix input and re-run: only b and c execute, a is reused
	failB = false
	rep2 := build().Run()
	if rep2.FirstBlocked != "" {
		t.Fatalf("still blocked: %q", rep2.FirstBlocked)
	}
	if rep2.Reused != 1 {
		t.Fatalf("reused = %d, want 1 (only a)", rep2.Reused)
	}
	if rep2.States["b"].Retries != 2 {
		t.Fatalf("b retries after recovery = %d, want 2", rep2.States["b"].Retries)
	}
	if rep2.States["c"].Status != "done" {
		t.Fatalf("c status = %s", rep2.States["c"].Status)
	}
}

func TestInputChangeInvalidatesDownstream(t *testing.T) {
	ckptPath := filepath.Join(t.TempDir(), "ckpt.json")
	input := "v1"
	build := func() *Scheduler {
		ckpt, _ := LoadCheckpoint(ckptPath)
		nodes := []*Node{
			mkNode("a", nil, input, func() (any, error) { return "ra", nil }),
			mkNode("b", []string{"a"}, "in-b", func() (any, error) { return "rb", nil }),
		}
		s, _ := NewScheduler(nodes, ckpt)
		return s
	}
	build().Run()
	input = "v2"
	rep := build().Run()
	if rep.Reused != 0 {
		t.Fatalf("reused = %d after input change, want 0", rep.Reused)
	}
}

func TestConcurrentDuplicateKeyRejected(t *testing.T) {
	s, err := NewScheduler([]*Node{
		mkNode("a", nil, "x", func() (any, error) { return "r", nil }),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := "fixed-key"
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.trySubmit(key) {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != 1 {
		t.Fatalf("admitted = %d, want exactly 1", admitted)
	}
}

func TestCycleRejected(t *testing.T) {
	_, err := NewScheduler([]*Node{
		mkNode("a", []string{"b"}, "x", nil),
		mkNode("b", []string{"a"}, "x", nil),
	}, nil)
	if err == nil {
		t.Fatal("expected cycle error")
	}
}
