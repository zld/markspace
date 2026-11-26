package graph

import (
	"reflect"
	"testing"
)

func TestBuildStableLevels(t *testing.T) {
	nodes := []Node{
		{ID: "report", Requires: []string{"audit"}},
		{ID: "scan-b", Requires: []string{}},
		{ID: "audit", Requires: []string{"repair-a", "repair-b"}},
		{ID: "scan-a"},
		{ID: "repair-a", Requires: []string{"scan-a"}},
		{ID: "repair-b", Requires: []string{"scan-b"}},
	}
	p, err := Build(nodes)
	if err != nil {
		t.Fatal(err)
	}
	wantLevels := [][]string{
		{"scan-b", "scan-a"}, // input order, not alphabetical
		{"repair-a", "repair-b"},
		{"audit"},
		{"report"},
	}
	if !reflect.DeepEqual(p.Levels, wantLevels) {
		t.Fatalf("levels=%#v want %#v", p.Levels, wantLevels)
	}
}

func TestBuildRejectsCycle(t *testing.T) {
	_, err := Build([]Node{
		{ID: "a", Requires: []string{"b"}},
		{ID: "b", Requires: []string{"c"}},
		{ID: "c", Requires: []string{"a"}},
	})
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if pe, ok := err.(interface{ Error() string }); !ok || pe.Error() == "" {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestSkipClosureIsTransitive(t *testing.T) {
	p, err := Build([]Node{
		{ID: "bad"},
		{ID: "align", Requires: []string{"bad"}},
		{ID: "mix", Requires: []string{"align"}},
		{ID: "ok", Requires: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := p.SkipClosure("bad")
	if !got["align"] || !got["mix"] || got["ok"] {
		t.Fatalf("closure=%v", got)
	}
}
