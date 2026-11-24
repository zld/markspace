package dag

import (
	"errors"
	"testing"

	mpierr "mediapipeline/internal/errors"
)

func TestTopologicalOrder(t *testing.T) {
	g, err := New([]Node{
		{Name: "parse", Deps: nil},
		{Name: "jitter", Deps: []string{"parse"}},
		{Name: "recover", Deps: []string{"jitter"}},
		{Name: "sync", Deps: []string{"recover"}},
		{Name: "mux", Deps: []string{"sync"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, n := range g.Order() {
		pos[n] = i
	}
	for _, pair := range [][2]string{{"parse", "jitter"}, {"jitter", "recover"}, {"recover", "sync"}, {"sync", "mux"}} {
		if pos[pair[0]] >= pos[pair[1]] {
			t.Fatalf("order violation: %s must precede %s in %v", pair[0], pair[1], g.Order())
		}
	}
}

func TestCycleRejected(t *testing.T) {
	_, err := New([]Node{
		{Name: "a", Deps: []string{"c"}},
		{Name: "b", Deps: []string{"a"}},
		{Name: "c", Deps: []string{"b"}},
	})
	var me *mpierr.MediaError
	if !errors.As(err, &me) || me.Category != mpierr.CatDAG {
		t.Fatalf("err = %v, want CatDAG", err)
	}
}

func TestUnknownDependencyRejected(t *testing.T) {
	_, err := New([]Node{
		{Name: "a"},
		{Name: "b", Deps: []string{"ghost"}},
	})
	var me *mpierr.MediaError
	if !errors.As(err, &me) || me.Category != mpierr.CatDAG {
		t.Fatalf("err = %v, want CatDAG", err)
	}
}

func TestDuplicateAndEmptyNamesRejected(t *testing.T) {
	if _, err := New([]Node{{Name: "x"}, {Name: "x"}}); err == nil {
		t.Fatal("duplicate names must be rejected")
	}
	if _, err := New([]Node{{Name: ""}}); err == nil {
		t.Fatal("empty name must be rejected")
	}
}

func TestNodeLimit(t *testing.T) {
	nodes := make([]Node, MaxNodes+1)
	for i := range nodes {
		nodes[i] = Node{Name: string(rune('a'+i%26)) + string(rune('a'+i/26)) + string(rune('0'+i%10))}
	}
	_, err := New(nodes)
	var me *mpierr.MediaError
	if !errors.As(err, &me) || me.Category != mpierr.CatLimit {
		t.Fatalf("err = %v, want CatLimit", err)
	}
}

func TestDeterministicTieOrdering(t *testing.T) {
	g, err := New([]Node{
		{Name: "zebra"}, {Name: "alpha"}, {Name: "mid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "mid", "zebra"}
	for i, n := range g.Order() {
		if n != want[i] {
			t.Fatalf("order = %v, want %v", g.Order(), want)
		}
	}
}
