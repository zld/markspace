package track

import (
	"testing"

	"mediacheck/internal/damage"
)

func TestPlanTopologicalOrder(t *testing.T) {
	g := NewGraph()
	g.AddEdge(Edge{TrackID: 3, DependsOn: 1, Kind: EdgeHint})
	g.AddEdge(Edge{TrackID: 3, DependsOn: 2, Kind: EdgeDescription})
	g.AddEdge(Edge{TrackID: 2, DependsOn: 1, Kind: EdgeDerived})
	plan := g.Plan()
	if len(plan.Stopped) != 0 {
		t.Fatalf("unexpected stops: %+v", plan.Stopped)
	}
	want := []uint32{1, 2, 3}
	for i, id := range want {
		if plan.Order[i] != id {
			t.Fatalf("order = %v, want %v", plan.Order, want)
		}
	}
}

func TestPlanDeterministicTieBreak(t *testing.T) {
	g := NewGraph()
	for _, id := range []uint32{7, 2, 5, 1} {
		g.AddTrack(id)
	}
	p1, p2 := g.Plan(), g.Plan()
	for i := range p1.Order {
		if p1.Order[i] != p2.Order[i] {
			t.Fatalf("plans diverged: %v vs %v", p1.Order, p2.Order)
		}
	}
	want := []uint32{1, 2, 5, 7}
	for i := range want {
		if p1.Order[i] != want[i] {
			t.Fatalf("order = %v, want ascending %v", p1.Order, want)
		}
	}
}

func TestPlanMissingDependencyStopsTrack(t *testing.T) {
	g := NewGraph()
	g.AddTrack(1)
	stop := g.MarkMissingDependency(4, 9)
	if stop.Code != damage.CodeDependencyMissing {
		t.Fatalf("code = %s, want dependency_missing", stop.Code)
	}
	// Re-run with an explicit dangling edge built by hand.
	g2 := NewGraph()
	g2.AddTrack(1)
	g2.AddManifestEdge(Edge{TrackID: 4, DependsOn: 9, Kind: EdgeExternal})
	plan := g2.Plan()
	found := false
	for _, s := range plan.Stopped {
		if s.TrackID == 4 && s.Code == damage.CodeDependencyMissing {
			found = true
		}
	}
	if !found {
		t.Fatalf("dangling edge must stop track 4, got %+v", plan.Stopped)
	}
	for _, id := range plan.Order {
		if id == 4 {
			t.Fatal("stopped track must never appear in the plan order")
		}
	}
}

func TestPlanMissingDependencyPropagatesDownstream(t *testing.T) {
	g := NewGraph()
	g.AddTrack(1)
	g.AddManifestEdge(Edge{TrackID: 2, DependsOn: 9}) // 2 blocked directly
	g.AddEdge(Edge{TrackID: 3, DependsOn: 2})         // 3 blocked transitively
	plan := g.Plan()
	stopped := map[uint32]bool{}
	for _, s := range plan.Stopped {
		stopped[s.TrackID] = true
	}
	if !stopped[2] || !stopped[3] {
		t.Fatalf("blockage must propagate to track 3, stops=%+v", plan.Stopped)
	}
	if len(plan.Order) != 1 || plan.Order[0] != 1 {
		t.Fatalf("only independent track 1 may be planned, got %v", plan.Order)
	}
}

func TestPlanCycleStopsAllParticipants(t *testing.T) {
	g := NewGraph()
	g.AddEdge(Edge{TrackID: 1, DependsOn: 2})
	g.AddEdge(Edge{TrackID: 2, DependsOn: 3})
	g.AddEdge(Edge{TrackID: 3, DependsOn: 1})
	g.AddTrack(9) // independent track still processes
	plan := g.Plan()
	if len(plan.Cycles) == 0 {
		t.Fatal("cycle must be reported")
	}
	stopped := map[uint32]bool{}
	for _, s := range plan.Stopped {
		if s.Code != damage.CodeDependencyCycle {
			t.Errorf("stop code = %s, want dependency_cycle", s.Code)
		}
		stopped[s.TrackID] = true
	}
	for _, id := range []uint32{1, 2, 3} {
		if !stopped[id] {
			t.Errorf("cyclic track %d must be stopped", id)
		}
	}
	if len(plan.Order) != 1 || plan.Order[0] != 9 {
		t.Fatalf("independent track must still plan, got %v", plan.Order)
	}
}

func TestKindFromString(t *testing.T) {
	if KindFromString("hint") != EdgeHint || KindFromString("cdsc") != EdgeDescription {
		t.Fatal("known tref types must map to typed edges")
	}
	if KindFromString("zzzz") != EdgeExternal {
		t.Fatal("unknown tref type must fall back to external")
	}
}
