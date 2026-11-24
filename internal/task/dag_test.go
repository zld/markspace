package task

import (
	"errors"
	"sync"
	"testing"

	"archivecheck/internal/merr"
)

func TestDAGRejectsCycleAndUnknownDep(t *testing.T) {
	if _, err := New([]*Node{
		{Key: "a", Deps: []string{"b"}},
		{Key: "b", Deps: []string{"a"}},
	}); err == nil {
		t.Fatal("cycle must be rejected before startup")
	} else if me, ok := merr.As(err); !ok || me.Kind != merr.KindDAG {
		t.Fatalf("wrong error kind: %v", err)
	}

	if _, err := New([]*Node{
		{Key: "a", Deps: []string{"missing"}},
	}); err == nil {
		t.Fatal("unknown dependency must be rejected")
	}
	if _, err := New([]*Node{{Key: "a"}, {Key: "a"}}); err == nil {
		t.Fatal("duplicate keys must be rejected")
	}
}

func TestRunDependencyOrderAndSuccess(t *testing.T) {
	var mu sync.Mutex
	var order []string
	mk := func(key string, deps ...string) *Node {
		return &Node{Key: key, Deps: deps, Run: func() error {
			mu.Lock()
			order = append(order, key)
			mu.Unlock()
			return nil
		}}
	}
	d, err := New([]*Node{
		mk("stats", "timeline"),
		mk("render", "timeline"),
		mk("timeline", "recover"),
		mk("recover", "manifest"),
		mk("manifest"),
	})
	if err != nil {
		t.Fatal(err)
	}
	res := d.Run(Options{MaxAttempts: 1})
	if res.Failed != 0 || res.Blocking != nil {
		t.Fatalf("unexpected failure: %+v", res.Blocking)
	}
	pos := map[string]int{}
	for i, k := range order {
		pos[k] = i
	}
	if !(pos["manifest"] < pos["recover"] && pos["recover"] < pos["timeline"] &&
		pos["timeline"] < pos["render"] && pos["timeline"] < pos["stats"]) {
		t.Fatalf("dependency order violated: %v", order)
	}
	for key, st := range res.States {
		if st.State != StatusSuccess {
			t.Fatalf("node %s not success: %s", key, st.State)
		}
	}
}

func TestFailedDependencyBlocksSuccessors(t *testing.T) {
	boom := merr.New(merr.KindTransport, "transport", "track:2:seq:1", "burst loss")
	d, err := New([]*Node{
		{Key: "manifest", Run: func() error { return nil }},
		{Key: "recover", Deps: []string{"manifest"}, Run: func() error { return boom }},
		{Key: "timeline", Deps: []string{"recover"}, Run: func() error { return nil }},
		{Key: "render", Deps: []string{"timeline"}, Run: func() error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := d.Run(Options{MaxAttempts: 1})
	if res.Failed != 1 {
		t.Fatalf("failed = %d, want 1", res.Failed)
	}
	if res.States["timeline"].State != StatusBlocked ||
		res.States["render"].State != StatusBlocked {
		t.Fatalf("successors must be blocked: %+v %+v",
			res.States["timeline"].State, res.States["render"].State)
	}
	if res.Blocking == nil || res.Blocking.BlockedBy != "recover" {
		t.Fatalf("first blocking dep wrong: %+v", res.Blocking)
	}
	if res.Blocking.Location != "track:2:seq:1" || res.Blocking.Subsystem != "transport" {
		t.Fatalf("blocking report lost error location: %+v", res.Blocking)
	}
}

func TestRetryIsIdempotentAndEventuallySucceeds(t *testing.T) {
	var calls int
	var mu sync.Mutex
	d, err := New([]*Node{
		{Key: "flaky", Run: func() error {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n < 3 {
				return errors.New("transient")
			}
			return nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := d.Run(Options{MaxAttempts: 3})
	if res.States["flaky"].State != StatusSuccess || res.States["flaky"].Attempts != 3 {
		t.Fatalf("retry did not converge: %+v", res.States["flaky"])
	}

	// Stable key rerun with a committed artifact executes nothing.
	res2 := d.Run(Options{MaxAttempts: 3, AlreadySucceeded: map[string]bool{"flaky": true}})
	if res2.Executed != 0 || res2.Succeeded != 1 {
		t.Fatalf("resume must skip committed nodes: executed=%d", res2.Executed)
	}
}

func TestInvalidatedAndResumePlan(t *testing.T) {
	d, err := New([]*Node{
		{Key: "manifest"},
		{Key: "recover-v0", Deps: []string{"manifest"}},
		{Key: "recover-v1", Deps: []string{"manifest"}},
		{Key: "timeline", Deps: []string{"recover-v0", "recover-v1"}},
		{Key: "render", Deps: []string{"timeline"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// v0 was repaired: v0, timeline and render must rerun; v1 stays.
	inv := d.Invalidated([]string{"recover-v0"})
	for _, k := range []string{"recover-v0", "timeline", "render"} {
		if !inv[k] {
			t.Fatalf("%s should be invalidated", k)
		}
	}
	if inv["recover-v1"] || inv["manifest"] {
		t.Fatal("unrelated branch must stay committed")
	}
	plan := d.ResumePlan(inv)
	if !plan["recover-v1"] || plan["render"] {
		t.Fatalf("resume plan wrong: %+v", plan)
	}
}

func TestStatesContainAllPhases(t *testing.T) {
	// Ensure the documented state constants exist with expected values.
	cases := map[Status]string{
		StatusPending: "pending", StatusRunning: "running",
		StatusSuccess: "success", StatusFailed: "failed", StatusBlocked: "blocked",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Fatalf("status = %q want %q", got, want)
		}
	}
}
