package graph

import (
	"testing"

	"mediapipe/internal/model"
)

func TestTopoOrderDeterministic(t *testing.T) {
	nodes := []Node{
		{ID: "mix", Stage: "mix", Deps: []string{"a-norm", "b-norm"}},
		{ID: "a-norm", Stage: "layout", Deps: []string{"a-verify"}},
		{ID: "b-norm", Stage: "layout", Deps: []string{"b-verify"}},
		{ID: "a-verify", Stage: "verify"},
		{ID: "b-verify", Stage: "verify"},
	}
	p, err := Build(nodes)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Order()
	want := []string{"a-verify", "a-norm", "b-verify", "b-norm", "mix"}
	if len(got) != len(want) {
		t.Fatalf("order = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	// Rebuilding with shuffled input must produce the same order.
	shuffled := []Node{nodes[3], nodes[0], nodes[4], nodes[2], nodes[1]}
	p2, err := Build(shuffled)
	if err != nil || !equal(p2.Order(), got) {
		t.Fatalf("schedule not stable across input order: %v, err=%v", p2, err)
	}
}

func TestGraphErrors(t *testing.T) {
	cases := []struct {
		name  string
		nodes []Node
		want  model.ErrorType
	}{
		{"duplicate", []Node{{ID: "x"}, {ID: "x"}}, model.ErrDuplicateNode},
		{"missing dep", []Node{{ID: "x", Deps: []string{"ghost"}}}, model.ErrMissingDep},
		{"cycle", []Node{{ID: "a", Deps: []string{"c"}}, {ID: "b", Deps: []string{"a"}}, {ID: "c", Deps: []string{"b"}}}, model.ErrGraphCycle},
		{"self cycle", []Node{{ID: "a", Deps: []string{"a"}}}, model.ErrGraphCycle},
		{"repeated dep", []Node{{ID: "a"}, {ID: "b", Deps: []string{"a", "a"}}}, model.ErrInvalidConfig},
	}
	for _, c := range cases {
		_, err := Build(c.nodes)
		me := model.AsMediaError(err)
		if me == nil || me.Type != c.want {
			t.Errorf("%s: got %v, want type %s", c.name, err, c.want)
		}
	}
}

func TestFailureBlocksDownstreamOnly(t *testing.T) {
	nodes := []Node{
		{ID: "v-verify", Stage: "verify"},
		{ID: "v-color", Stage: "color", Deps: []string{"v-verify"}},
		{ID: "v-cut", Stage: "cut", Deps: []string{"v-color"}},
		{ID: "a-verify", Stage: "verify"},
		{ID: "a-layout", Stage: "layout", Deps: []string{"a-verify"}},
	}
	p, err := Build(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if ds := p.Downstream("v-verify"); !equal(ds, []string{"v-color", "v-cut"}) {
		t.Fatalf("downstream = %v", ds)
	}
	runnable, blocked := p.Schedule(
		map[string]bool{"v-verify": true},
		map[string]bool{},
	)
	// Nothing failed: every unfinished node is runnable in topo order.
	if !equal(runnable, []string{"a-verify", "a-layout", "v-color", "v-cut"}) || len(blocked) != 0 {
		t.Fatalf("healthy schedule runnable=%v blocked=%v", runnable, blocked)
	}

	runnable, blocked = p.Schedule(
		map[string]bool{"v-verify": true},
		map[string]bool{"a-verify": true},
	)
	// After a-verify fails: its chain is blocked; the unaffected video
	// chain remains runnable.
	if !equal(runnable, []string{"v-color", "v-cut"}) || !equal(blocked, []string{"a-layout"}) {
		t.Fatalf("after failure runnable=%v blocked=%v", runnable, blocked)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
