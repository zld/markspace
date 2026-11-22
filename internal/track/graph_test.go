package track

import (
	"strings"
	"testing"

	"mediadist/internal/merr"
)

func graphWith(t *testing.T, tracks ...Track) *Graph {
	t.Helper()
	g := NewGraph()
	for _, tr := range tracks {
		if err := g.Add(tr); err != nil {
			t.Fatalf("add %d: %v", tr.ID, err)
		}
	}
	return g
}

func TestTopoSortPutsDependenciesFirst(t *testing.T) {
	g := graphWith(t,
		Track{ID: 3, Dependencies: []uint32{1}},
		Track{ID: 1},
		Track{ID: 2, Dependencies: []uint32{3}},
	)
	order, err := g.TopoSort()
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	pos := map[uint32]int{}
	for i, id := range order {
		pos[id] = i
	}
	if pos[1] > pos[3] || pos[3] > pos[2] {
		t.Fatalf("order %v violates dependencies", order)
	}
}

func TestTopoSortIsDeterministic(t *testing.T) {
	build := func() *Graph {
		return graphWith(t,
			Track{ID: 4}, Track{ID: 2}, Track{ID: 3}, Track{ID: 1},
		)
	}
	first, err := build().TopoSort()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		next, err := build().TopoSort()
		if err != nil {
			t.Fatal(err)
		}
		for j := range first {
			if first[j] != next[j] {
				t.Fatalf("run %d produced %v want %v", i, next, first)
			}
		}
	}
	// With no dependencies the order is ascending track ID.
	for i, id := range first {
		if id != uint32(i+1) {
			t.Fatalf("order %v", first)
		}
	}
}

func TestCycleIsRejectedWithLoopTracks(t *testing.T) {
	g := graphWith(t,
		Track{ID: 1, Dependencies: []uint32{3}},
		Track{ID: 2, Dependencies: []uint32{1}},
		Track{ID: 3, Dependencies: []uint32{2}},
	)
	_, err := g.TopoSort()
	if err == nil {
		t.Fatal("cycle must be rejected")
	}
	if !IsCycle(err) {
		t.Fatalf("error is not a CycleError: %v", err)
	}
	ce := err.(*CycleError)
	if len(ce.Cycle) < 4 {
		t.Fatalf("cycle %v should close the loop", ce.Cycle)
	}
	if ce.Cycle[0] != ce.Cycle[len(ce.Cycle)-1] {
		t.Fatalf("cycle %v is not closed", ce.Cycle)
	}
	for _, id := range ce.Cycle {
		if id < 1 || id > 3 {
			t.Fatalf("cycle contains unknown track %d", id)
		}
	}
}

func TestSelfDependencyIsIgnored(t *testing.T) {
	g := graphWith(t, Track{ID: 1, Dependencies: []uint32{1}})
	order, err := g.TopoSort()
	if err != nil {
		t.Fatalf("self edge should be dropped: %v", err)
	}
	if len(order) != 1 || order[0] != 1 {
		t.Fatalf("order %v", order)
	}
}

func TestMissingDependencyIsRecoverable(t *testing.T) {
	g := graphWith(t, Track{ID: 1, Dependencies: []uint32{99}})
	err := g.Validate()
	if err == nil {
		t.Fatal("dangling dependency must be reported")
	}
	if !strings.Contains(err.Error(), "unknown track 99") {
		t.Fatalf("error %v", err)
	}
	if e := merr.AsError(err); e == nil || !e.Recovered {
		t.Fatalf("missing dependency should be recoverable: %v", err)
	}
}

func TestDuplicateAndReservedTrackIDs(t *testing.T) {
	g := NewGraph()
	if err := g.Add(Track{ID: 0}); err == nil {
		t.Fatal("track 0 must be reserved")
	}
	if err := g.Add(Track{ID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := g.Add(Track{ID: 1}); err == nil {
		t.Fatal("duplicate ID must be rejected")
	}
}

func TestBuildMappingOrdersAndInverts(t *testing.T) {
	g := graphWith(t,
		Track{ID: 1}, Track{ID: 2}, Track{ID: 3, Dependencies: []uint32{1}},
	)
	m, err := BuildMapping(g, []MapEntry{
		{InputID: 3, OutputID: 30},
		{InputID: 1, OutputID: 10},
		{InputID: 2, OutputID: 20},
	}, UnknownError)
	if err != nil {
		t.Fatalf("BuildMapping: %v", err)
	}
	if out, ok := m.OutputFor(3); !ok || out != 30 {
		t.Fatalf("output for 3: %d %v", out, ok)
	}
	if in, ok := m.ToInput[30]; !ok || in != 3 {
		t.Fatalf("inverse for 30: %d %v", in, ok)
	}
	// Order follows topological order: 1 before 3, and 2 anywhere after 1.
	pos := map[uint32]int{}
	for i, id := range m.Order {
		pos[id] = i
	}
	if pos[10] > pos[30] {
		t.Fatalf("order %v violates dependency", m.Order)
	}
}

func TestBuildMappingDropsAndErrors(t *testing.T) {
	g := graphWith(t, Track{ID: 1}, Track{ID: 2})
	entries := []MapEntry{{InputID: 1, OutputID: 1}}

	if _, err := BuildMapping(g, entries, UnknownError); err == nil {
		t.Fatal("unmapped track must fail under the error policy")
	}
	m, err := BuildMapping(g, entries, UnknownDrop)
	if err != nil {
		t.Fatalf("drop policy: %v", err)
	}
	if !m.IsDropped(2) {
		t.Fatalf("track 2 should be dropped: %+v", m)
	}

	// An explicit Drop entry is honored even under the error policy.
	m2, err := BuildMapping(g, []MapEntry{
		{InputID: 1, OutputID: 1},
		{InputID: 2, Drop: true},
	}, UnknownError)
	if err != nil {
		t.Fatalf("explicit drop: %v", err)
	}
	if !m2.IsDropped(2) {
		t.Fatalf("explicitly dropped track appears mapped")
	}
}

func TestBuildMappingRejectsBadEntries(t *testing.T) {
	g := graphWith(t, Track{ID: 1}, Track{ID: 2})
	cases := []struct {
		name    string
		entries []MapEntry
	}{
		{"unknown input", []MapEntry{{InputID: 9, OutputID: 9}, {InputID: 1, OutputID: 1}, {InputID: 2, OutputID: 2}}},
		{"duplicate output", []MapEntry{{InputID: 1, OutputID: 5}, {InputID: 2, OutputID: 5}}},
		{"duplicate input", []MapEntry{{InputID: 1, OutputID: 1}, {InputID: 1, OutputID: 2}, {InputID: 2, OutputID: 2}}},
		{"zero output", []MapEntry{{InputID: 1, OutputID: 0}, {InputID: 2, OutputID: 2}}},
	}
	for _, c := range cases {
		if _, err := BuildMapping(g, c.entries, UnknownError); err == nil {
			t.Fatalf("%s: expected rejection", c.name)
		}
	}
}

func TestCyclePreventsMapping(t *testing.T) {
	g := graphWith(t,
		Track{ID: 1, Dependencies: []uint32{2}},
		Track{ID: 2, Dependencies: []uint32{1}},
	)
	_, err := BuildMapping(g, nil, UnknownDrop)
	if err == nil {
		t.Fatal("mapping over a cyclic graph must be refused")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error %v should mention the cycle", err)
	}
}

func TestRequestedMissing(t *testing.T) {
	m := &Mapping{ToOutput: map[uint32]uint32{1: 1}, ToInput: map[uint32]uint32{1: 1}}
	m.RequestedMissing([]uint32{1, 2, 3})
	if len(m.Missing) != 2 || m.Missing[0] != 2 || m.Missing[1] != 3 {
		t.Fatalf("missing %v", m.Missing)
	}
}

func TestUnknownPolicyParsing(t *testing.T) {
	for _, s := range []string{"", "error"} {
		if p, err := ParseUnknownPolicy(s); err != nil || p != UnknownError {
			t.Fatalf("%q -> %v %v", s, p, err)
		}
	}
	if p, err := ParseUnknownPolicy("drop"); err != nil || p != UnknownDrop {
		t.Fatalf("drop -> %v %v", p, err)
	}
	if _, err := ParseUnknownPolicy("ignore"); err == nil {
		t.Fatal("invalid policy must fail")
	}
}
