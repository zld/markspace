package graph_test

import (
	"testing"

	"mediasync/internal/graph"
	"mediasync/internal/model"
)

func TestPlanTopologicalAndStable(t *testing.T) {
	nodes := []graph.Node{
		{TrackID: "subtitle", DependsOn: []string{"dialogue"}},
		{TrackID: "dialogue"},
		{TrackID: "sfx", DependsOn: []string{"dialogue"}},
	}
	o := graph.Plan(nodes)
	if len(o.Cycle) != 0 || len(o.Unsatisfied) != 0 {
		t.Fatalf("unexpected cycle/unsatisfied: %+v %+v", o.Cycle, o.Unsatisfied)
	}
	pos := map[string]int{}
	for i, id := range o.Ordered {
		pos[id] = i
	}
	if pos["dialogue"] >= pos["subtitle"] || pos["dialogue"] >= pos["sfx"] {
		t.Fatalf("dependency order violated: %v", o.Ordered)
	}
	// Independent peers sorted by ID: sfx before subtitle.
	if o.Ordered[0] != "dialogue" || o.Ordered[1] != "sfx" || o.Ordered[2] != "subtitle" {
		t.Fatalf("unstable order: %v", o.Ordered)
	}
}

func TestPlanDetectsCycle(t *testing.T) {
	nodes := []graph.Node{
		{TrackID: "a", DependsOn: []string{"c"}},
		{TrackID: "b", DependsOn: []string{"a"}},
		{TrackID: "c", DependsOn: []string{"b"}},
		{TrackID: "independent"},
	}
	o := graph.Plan(nodes)
	want := map[string]bool{"a": true, "b": true, "c": true}
	if len(o.Cycle) != 3 {
		t.Fatalf("cycle = %v", o.Cycle)
	}
	for _, id := range o.Cycle {
		if !want[id] {
			t.Fatalf("unexpected cycle member %s", id)
		}
	}
	// The independent track still schedules.
	if len(o.Ordered) != 1 || o.Ordered[0] != "independent" {
		t.Fatalf("independent track blocked: %v", o.Ordered)
	}
	f := o.CycleFailure()
	if f == nil || f.Code != model.ErrGraphCycle {
		t.Fatalf("cycle failure = %+v", f)
	}
}

func TestPlanUnknownDependency(t *testing.T) {
	nodes := []graph.Node{
		{TrackID: "sub", DependsOn: []string{"ghost"}},
		{TrackID: "dialogue"},
	}
	o := graph.Plan(nodes)
	if o.Unsatisfied["sub"] != "ghost" {
		t.Fatalf("unsatisfied = %+v", o.Unsatisfied)
	}
	for _, id := range o.Ordered {
		if id == "sub" {
			t.Fatal("track with unsatisfied dependency must not be scheduled")
		}
	}
}

func TestPlanDeterministicAcrossRuns(t *testing.T) {
	nodes := []graph.Node{
		{TrackID: "z", DependsOn: []string{"a", "m"}},
		{TrackID: "a"},
		{TrackID: "m"},
	}
	first := graph.Plan(nodes).Ordered
	for i := 0; i < 5; i++ {
		got := graph.Plan(nodes).Ordered
		if len(got) != len(first) {
			t.Fatal("length mismatch")
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("nondeterministic order: %v vs %v", got, first)
			}
		}
	}
}
