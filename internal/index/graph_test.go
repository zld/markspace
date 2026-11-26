package index

import (
	"errors"
	"testing"

	"regress/internal/media"
)

// memChecker is an in-memory Checker for tests.
type memChecker struct {
	records map[string]storedRec
}

type storedRec struct {
	input string
	deps  map[string]string
	res   string
}

func (m *memChecker) Lookup(id, input string, deps map[string]string) (string, []byte, bool, error) {
	rec, ok := m.records[id]
	if !ok || rec.input != input {
		return "", nil, false, nil
	}
	for k, v := range deps {
		if rec.deps[k] != v {
			return "", nil, false, nil
		}
	}
	if len(rec.deps) != len(deps) {
		return "", nil, false, nil
	}
	return rec.res, []byte(rec.res), true, nil
}

func countingCompute(calls *int, digest string) func(map[string]string) (string, []byte, error) {
	return func(map[string]string) (string, []byte, error) {
		*calls++
		return digest, []byte(digest), nil
	}
}

func TestTopologicalOrder(t *testing.T) {
	g, err := NewGraph([]*Node{
		{ID: "manifest", Kind: KindManifest, Input: "i", Deps: []string{"b", "a"},
			Boundary: Boundary{Start: media.NewInt(0), End: media.NewInt(10)},
			Compute:  countingCompute(new(int), "m")},
		{ID: "b", Kind: KindRTP, Input: "i", Deps: []string{"a"},
			Boundary: Boundary{Start: media.NewInt(0), End: media.NewInt(10)},
			Compute:  countingCompute(new(int), "b")},
		{ID: "a", Kind: KindSubtitle, Input: "i",
			Boundary: Boundary{Start: media.NewInt(0), End: media.NewInt(10)},
			Compute:  countingCompute(new(int), "a")},
	})
	if err != nil {
		t.Fatal(err)
	}
	order := g.Order()
	if len(order) != 3 || order[0] != "a" || order[2] != "manifest" {
		t.Fatalf("order=%v, want a before b before manifest", order)
	}
}

func TestUnknownDependencyAndCycleRejected(t *testing.T) {
	if _, err := NewGraph([]*Node{
		{ID: "x", Deps: []string{"ghost"}},
	}); err == nil {
		t.Fatal("unknown dependency accepted")
	}
	if _, err := NewGraph([]*Node{
		{ID: "x", Deps: []string{"y"}},
		{ID: "y", Deps: []string{"x"}},
	}); err == nil {
		t.Fatal("cycle accepted")
	}
}

func TestColdRunRebuildsAll(t *testing.T) {
	calls := map[string]*int{}
	mk := func(id string, deps []string) *Node {
		c := new(int)
		calls[id] = c
		return &Node{ID: id, Kind: KindSubtitle, Input: "in:" + id, Deps: deps,
			Boundary: Boundary{Start: media.NewInt(0), End: media.NewInt(1)},
			Compute:  countingCompute(c, "res:"+id)}
	}
	g, _ := NewGraph([]*Node{mk("a", nil), mk("b", []string{"a"})})
	out, err := g.Run(nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Rebuilt != 2 {
		t.Fatalf("rebuilt=%d, want 2", out.Rebuilt)
	}
	if *calls["a"] != 1 || *calls["b"] != 1 {
		t.Fatal("compute must run once per node on a cold run")
	}
}

func TestWarmRunReusesAll(t *testing.T) {
	callsA, callsB := new(int), new(int)
	nodes := []*Node{
		{ID: "a", Input: "in:a", Compute: countingCompute(callsA, "res:a"),
			Boundary: Boundary{Start: media.NewInt(0), End: media.NewInt(1)}},
		{ID: "b", Input: "in:b", Deps: []string{"a"}, Compute: countingCompute(callsB, "res:b"),
			Boundary: Boundary{Start: media.NewInt(1), End: media.NewInt(2)}},
	}
	g, _ := NewGraph(nodes)
	checker := &memChecker{records: map[string]storedRec{
		"a": {input: "in:a", deps: map[string]string{}, res: "res:a"},
		"b": {input: "in:b", deps: map[string]string{"a": "res:a"}, res: "res:b"},
	}}
	out, err := g.Run(checker)
	if err != nil {
		t.Fatal(err)
	}
	if out.Rebuilt != 0 {
		t.Fatalf("rebuilt=%d, want 0 on full reuse", out.Rebuilt)
	}
	if *callsA != 0 || *callsB != 0 {
		t.Fatal("compute must not run when checkpoints are valid")
	}
	for _, r := range out.Results {
		if !r.Reused {
			t.Fatalf("node %s not marked reused", r.NodeID)
		}
	}
}

func TestChangedInputRebuildsDownstreamOnly(t *testing.T) {
	callsA, callsB, callsC := new(int), new(int), new(int)
	// a -> b ; a -> c. Changing a invalidates b and c.
	nodes := []*Node{
		{ID: "a", Input: "in:a:v2", Compute: countingCompute(callsA, "res:a:v2"),
			Boundary: Boundary{Start: media.NewInt(0), End: media.NewInt(1)}},
		{ID: "b", Input: "in:b", Deps: []string{"a"}, Compute: countingCompute(callsB, "res:b"),
			Boundary: Boundary{Start: media.NewInt(1), End: media.NewInt(2)}},
		{ID: "c", Input: "in:c", Deps: []string{"a"}, Compute: countingCompute(callsC, "res:c"),
			Boundary: Boundary{Start: media.NewInt(2), End: media.NewInt(3)}},
	}
	g, _ := NewGraph(nodes)
	// Stale checkpoints: a was computed from the old input, and b/c from the
	// old a result. None should validate.
	checker := &memChecker{records: map[string]storedRec{
		"a": {input: "in:a:v1", deps: map[string]string{}, res: "res:a:v1"},
		"b": {input: "in:b", deps: map[string]string{"a": "res:a:v1"}, res: "res:b"},
		"c": {input: "in:c", deps: map[string]string{"a": "res:a:v1"}, res: "res:c"},
	}}
	out, err := g.Run(checker)
	if err != nil {
		t.Fatal(err)
	}
	if out.Rebuilt != 3 {
		t.Fatalf("rebuilt=%d, want 3 (changed node plus both dependents)", out.Rebuilt)
	}
}

func TestContinuityBreakDetected(t *testing.T) {
	nodes := []*Node{
		{ID: "a", Input: "in", Compute: countingCompute(new(int), "ra"),
			Boundary: Boundary{Start: media.NewInt(0), End: media.NewInt(10)}},
		// b opens at t=2 while its upstream a is anchored at t=0: a shifted
		// origin means the representation edges do not line up.
		{ID: "b", Input: "in", Deps: []string{"a"}, Compute: countingCompute(new(int), "rb"),
			Boundary: Boundary{Start: media.NewInt(2), End: media.NewInt(20)}},
	}
	g, _ := NewGraph(nodes)
	out, err := g.Run(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Breaks) != 1 {
		t.Fatalf("breaks=%v, want one", out.Breaks)
	}
	br := out.Breaks[0]
	if br.NodeID != "b" || br.Expected.String() != "0" || br.Got.String() != "2" {
		t.Fatalf("break wrong: %+v", br)
	}
	if out.Results[1].Continuity != ContinuityBroken {
		t.Fatal("node b must be marked broken")
	}
}

func TestAlignedBoundariesAreOK(t *testing.T) {
	nodes := []*Node{
		{ID: "a", Input: "in", Compute: countingCompute(new(int), "ra"),
			Boundary: Boundary{Start: media.NewInt(0), End: media.NewRat(1, 2)}},
		// b shares the same t=0 origin even though its span differs.
		{ID: "b", Input: "in", Deps: []string{"a"}, Compute: countingCompute(new(int), "rb"),
			Boundary: Boundary{Start: media.NewInt(0), End: media.NewInt(1)}},
	}
	g, _ := NewGraph(nodes)
	out, err := g.Run(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Breaks) != 0 {
		t.Fatalf("unexpected breaks: %v", out.Breaks)
	}
}

func TestComputeErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	g, _ := NewGraph([]*Node{
		{ID: "a", Input: "in",
			Compute:  func(map[string]string) (string, []byte, error) { return "", nil, boom },
			Boundary: Boundary{Start: media.NewInt(0), End: media.NewInt(1)}},
	})
	if _, err := g.Run(nil); !errors.Is(err, boom) {
		t.Fatalf("err=%v, want wrapped boom", err)
	}
}
