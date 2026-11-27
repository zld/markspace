package pipeline

import (
	"errors"
	"testing"

	"continuity/internal/timeline"
)

func node(id string, deps ...string) timeline.Node {
	return timeline.Node{ID: id, Stage: timeline.StageScan, Deps: deps}
}

func TestTopoOrder(t *testing.T) {
	p, err := New(
		&Step{Node: node("manifest", "damage", "keyframe")},
		&Step{Node: node("damage", "scan")},
		&Step{Node: node("scan")},
		&Step{Node: node("keyframe", "scan")},
	)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Order()
	want := []string{"scan", "damage", "keyframe", "manifest"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order=%v want %v", got, want)
		}
	}
}

func TestUnknownDepAndCycle(t *testing.T) {
	if _, err := New(&Step{Node: node("a", "nope")}); err == nil {
		t.Fatal("expected unknown dep error")
	}
	if _, err := New(
		&Step{Node: node("a", "b")},
		&Step{Node: node("b", "a")},
	); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestDependencyFailureSkipsDownstream(t *testing.T) {
	boom := errors.New("scan failed")
	p, err := New(
		&Step{Node: node("scan"), Fn: func(*Context) error { return boom }},
		&Step{Node: node("damage", "scan"), Fn: func(*Context) error {
			t.Fatal("damage must not run")
			return nil
		}},
		&Step{Node: node("manifest", "damage"), Fn: func(*Context) error {
			t.Fatal("manifest must not run")
			return nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	res := p.Run(NewContext(), nil)
	if res["scan"].Status != StatusFailed {
		t.Fatalf("scan: %+v", res["scan"])
	}
	if res["damage"].Status != StatusSkipped || res["manifest"].Status != StatusSkipped {
		t.Fatalf("downstream must be skipped: %+v", res)
	}
}

type memCP struct{ done map[string]bool }

func (m *memCP) Done(id string) bool { return m.done[id] }
func (m *memCP) Record(id string) error {
	m.done[id] = true
	return nil
}

func TestResumeUsesCheckpoint(t *testing.T) {
	ran := map[string]int{}
	mk := func(id string, deps ...string) *Step {
		return &Step{Node: node(id, deps...), Fn: func(*Context) error {
			ran[id]++
			return nil
		}}
	}
	p, err := New(mk("scan"), mk("damage", "scan"), mk("manifest", "damage"))
	if err != nil {
		t.Fatal(err)
	}
	cp := &memCP{done: map[string]bool{"scan": true}}
	res := p.Run(NewContext(), cp)
	if res["scan"].Status != StatusCached {
		t.Fatalf("scan should be cached: %+v", res["scan"])
	}
	if ran["scan"] != 0 || ran["damage"] != 1 || ran["manifest"] != 1 {
		t.Fatalf("ran=%v", ran)
	}
	// Second run: everything cached, nothing re-executes.
	res = p.Run(NewContext(), cp)
	for id, r := range res {
		if r.Status != StatusCached {
			t.Fatalf("step %s not cached on rerun: %+v", id, r)
		}
	}
	if ran["damage"] != 1 || ran["manifest"] != 1 {
		t.Fatalf("steps re-executed: %v", ran)
	}
}
