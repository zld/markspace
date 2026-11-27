package timeline

import (
	"testing"

	"mediad/internal/model"
)

func mediaGraph() *Graph {
	return &Graph{
		Nodes: []Node{
			{ID: "a1", Kind: KindAudio},
			{ID: "v1", Kind: KindVideo},
			{ID: "m", Kind: KindManifest},
		},
		Edges: []Edge{{From: "a1", To: "m"}, {From: "v1", To: "m"}},
	}
}

func TestTopologicalOrderStable(t *testing.T) {
	g := mediaGraph()
	order, err := g.Validate()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a1", "v1", "m"}
	if len(order) != 3 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("order = %v want %v", order, want)
	}
	// Reversed insertion must not change the lexicographic result.
	g2 := &Graph{
		Nodes: []Node{{ID: "m", Kind: KindManifest}, {ID: "v1", Kind: KindVideo}, {ID: "a1", Kind: KindAudio}},
		Edges: []Edge{{From: "v1", To: "m"}, {From: "a1", To: "m"}},
	}
	order2, err := g2.Validate()
	if err != nil || order2[0] != "a1" || order2[2] != "m" {
		t.Fatalf("unstable order: %v, %v", order2, err)
	}
}

func TestGraphMissingDependency(t *testing.T) {
	g := mediaGraph()
	g.Edges = append(g.Edges, Edge{From: "ghost", To: "m"})
	_, err := g.Validate()
	if model.AsError(err).Code != model.ErrGraphMissing {
		t.Fatalf("want ErrGraphMissing, got %v", err)
	}
}

func TestGraphManifestCoverage(t *testing.T) {
	g := &Graph{
		Nodes: []Node{{ID: "a1", Kind: KindAudio}, {ID: "m", Kind: KindManifest}},
		Edges: nil, // a1 never reaches the manifest
	}
	_, err := g.Validate()
	if model.AsError(err).Code != model.ErrGraphMissing {
		t.Fatalf("want ErrGraphMissing for uncovered media, got %v", err)
	}
}

func TestGraphCycleRejected(t *testing.T) {
	g := &Graph{
		Nodes: []Node{{ID: "a1", Kind: KindAudio}, {ID: "a2", Kind: KindAudio}, {ID: "m", Kind: KindManifest}},
		Edges: []Edge{{From: "a1", To: "a2"}, {From: "a2", To: "a1"}, {From: "a1", To: "m"}, {From: "a2", To: "m"}},
	}
	_, err := g.Validate()
	me := model.AsError(err)
	if me.Code != model.ErrGraphCycle || me.Track == "" {
		t.Fatalf("want located ErrGraphCycle, got %v", err)
	}
}

func TestGraphNoManifest(t *testing.T) {
	g := &Graph{Nodes: []Node{{ID: "a1", Kind: KindAudio}}}
	if _, err := g.Validate(); model.AsError(err).Code != model.ErrGraphMissing {
		t.Fatalf("got %v", err)
	}
}
