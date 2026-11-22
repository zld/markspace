package timeline

import (
	"reflect"
	"testing"
)

func TestTopoSortLinearAndDiamond(t *testing.T) {
	// diamond: a -> b, a -> c, b -> d, c -> d ; d also depends on a transitively
	tracks := []*Track{
		{ID: "d", Rate: 48000, Channels: 1, DependsOn: []string{"b", "c"}},
		{ID: "c", Rate: 48000, Channels: 1, DependsOn: []string{"a"}},
		{ID: "b", Rate: 48000, Channels: 1, DependsOn: []string{"a"}},
		{ID: "a", Rate: 48000, Channels: 1},
	}
	g, err := BuildGraph(tracks)
	if err != nil {
		t.Fatal(err)
	}
	order, err := g.TopoSort()
	if err != nil {
		t.Fatalf("diamond has no cycle: %v", err)
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	for _, pair := range [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}, {"c", "d"}} {
		if pos[pair[0]] >= pos[pair[1]] {
			t.Fatalf("topo order %v violates %s before %s", order, pair[0], pair[1])
		}
	}
	if want := []string{"a", "b", "c", "d"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("lexicographic tie-break order = %v, want %v", order, want)
	}
	if ups := g.Upstreams("d"); !reflect.DeepEqual(ups, []string{"b", "c"}) {
		t.Fatalf("upstreams(d) = %v", ups)
	}
	if downs := g.Downstreams("a"); !reflect.DeepEqual(downs, []string{"b", "c"}) {
		t.Fatalf("downstreams(a) = %v", downs)
	}
}

func TestTopoSortReportsCycle(t *testing.T) {
	tracks := []*Track{
		{ID: "a", Rate: 48000, Channels: 1, DependsOn: []string{"c"}},
		{ID: "b", Rate: 48000, Channels: 1, DependsOn: []string{"a"}},
		{ID: "c", Rate: 48000, Channels: 1, DependsOn: []string{"b"}},
		{ID: "d", Rate: 48000, Channels: 1}, // independent node still sortable
	}
	g, err := BuildGraph(tracks)
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.TopoSort()
	pe, ok := AsError(err)
	if !ok || pe.Code != ErrCodeCycle {
		t.Fatalf("got %v, want dependency_cycle", err)
	}
}

func TestBuildGraphRejectsUnknownAndSelf(t *testing.T) {
	bad := []*Track{{ID: "a", Rate: 48000, Channels: 1, DependsOn: []string{"ghost"}}}
	if _, err := BuildGraph(bad); err == nil {
		t.Fatal("unknown upstream must be rejected")
	}
	self := []*Track{{ID: "a", Rate: 48000, Channels: 1, DependsOn: []string{"a"}}}
	if _, err := BuildGraph(self); err == nil {
		t.Fatal("self dependency must be rejected")
	}
}

func TestTopoSortEmptyGraph(t *testing.T) {
	g, err := BuildGraph(nil)
	if err != nil {
		t.Fatal(err)
	}
	order, err := g.TopoSort()
	if err != nil || len(order) != 0 {
		t.Fatalf("empty graph: order=%v err=%v", order, err)
	}
}
