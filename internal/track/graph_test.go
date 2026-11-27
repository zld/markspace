package track

import (
	"errors"
	"testing"

	"medtrace/internal/mederr"
)

func TestTopoSortBasic(t *testing.T) {
	g, err := NewGraph([]Track{
		{ID: 3, Kind: KindAudio, CodecID: "A_OPUS"},
		{ID: 1, Kind: KindVideo, CodecID: "V_VP9"},
		{ID: 2, Kind: KindVideo, CodecID: "V_VP9_ALPHA", DependsOn: []uint64{1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := g.TopoSort()
	if err != nil {
		t.Fatal(err)
	}
	// Track 1 must precede its alpha track 2; smallest ready ID wins ties.
	want := []uint64{1, 2, 3}
	if len(order) != len(want) {
		t.Fatalf("order=%v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order=%v want=%v", order, want)
		}
	}
}

func TestClockDependency(t *testing.T) {
	g, err := NewGraph([]Track{
		{ID: 1, Kind: KindVideo, CodecID: "V_VP9"},
		{ID: 2, Kind: KindAudio, CodecID: "A_OPUS", ClockRefs: []uint64{1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := g.TopoSort()
	if err != nil || len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("clock-slaved track must follow master, order=%v err=%v", order, err)
	}
}

func TestCycleReported(t *testing.T) {
	g, err := NewGraph([]Track{
		{ID: 1, DependsOn: []uint64{2}},
		{ID: 2, DependsOn: []uint64{3}},
		{ID: 3, DependsOn: []uint64{1}},
		{ID: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.TopoSort()
	se := mederr.AsError(err)
	if se == nil || se.Category != mederr.CatCycle {
		t.Fatalf("want dependency_cycle, got %v", err)
	}
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Fatalf("cycle must wrap CycleError, got %v", err)
	}
	want := map[uint64]bool{1: true, 2: true, 3: true}
	if len(ce.Tracks) != 3 {
		t.Fatalf("cycle tracks=%v", ce.Tracks)
	}
	for _, id := range ce.Tracks {
		if !want[id] {
			t.Fatalf("unexpected track %d in cycle %v", id, ce.Tracks)
		}
	}
}

func TestSelfDependencyIgnored(t *testing.T) {
	g, err := NewGraph([]Track{{ID: 1, DependsOn: []uint64{1}}})
	if err != nil {
		t.Fatal(err)
	}
	order, err := g.TopoSort()
	if err != nil || len(order) != 1 || order[0] != 1 {
		t.Fatalf("self dep must not form a cycle, order=%v err=%v", order, err)
	}
}

func TestUnknownDependency(t *testing.T) {
	_, err := NewGraph([]Track{{ID: 1, DependsOn: []uint64{9}}})
	if mederr.AsError(err) == nil {
		t.Fatal("unknown dependency must fail")
	}
}

func TestDuplicateTrack(t *testing.T) {
	_, err := NewGraph([]Track{{ID: 1}, {ID: 1}})
	if mederr.AsError(err) == nil {
		t.Fatal("duplicate track id must fail")
	}
}

func TestDiamondOrdering(t *testing.T) {
	// 1 -> {2,3} -> 4: both branches before the merge, ties by id.
	g, err := NewGraph([]Track{
		{ID: 4, DependsOn: []uint64{2, 3}},
		{ID: 2, DependsOn: []uint64{1}},
		{ID: 3, DependsOn: []uint64{1}},
		{ID: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := g.TopoSort()
	if err != nil {
		t.Fatal(err)
	}
	pos := map[uint64]int{}
	for i, id := range order {
		pos[id] = i
	}
	if pos[1] > pos[2] || pos[1] > pos[3] || pos[2] > pos[4] || pos[3] > pos[4] {
		t.Fatalf("diamond order violated: %v", order)
	}
}
