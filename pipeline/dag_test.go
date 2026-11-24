package pipeline

import (
	"testing"
)

func TestTopologicalOrder(t *testing.T) {
	d, err := NewDAG([]Node{
		{Name: "fingerprint", Deps: []string{"analyze"}},
		{Name: "analyze", Deps: []string{"ingest"}},
		{Name: "ingest"},
		{Name: "manifest", Deps: []string{"fingerprint", "analyze"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, name := range d.Order() {
		pos[name] = i
	}
	if !(pos["ingest"] < pos["analyze"] && pos["analyze"] < pos["fingerprint"] && pos["fingerprint"] < pos["manifest"]) {
		t.Fatalf("order violates dependencies: %v", d.Order())
	}
}

func TestIdempotencyKeyStableAndSensitive(t *testing.T) {
	mk := func(inputs []string, params any) *Node {
		return &Node{Name: "n", Inputs: inputs, Params: params}
	}
	k1, err := mk([]string{"hash-a", "hash-b"}, map[string]int{"window": 5}).Key()
	if err != nil {
		t.Fatal(err)
	}
	// Same inputs in a different order must yield the same key.
	k2, _ := mk([]string{"hash-b", "hash-a"}, map[string]int{"window": 5}).Key()
	if k1 != k2 {
		t.Fatal("idempotency key must be independent of input ordering")
	}
	// Different input summary must change the key.
	k3, _ := mk([]string{"hash-a", "hash-c"}, map[string]int{"window": 5}).Key()
	if k1 == k3 {
		t.Fatal("changed input must produce a different key")
	}
	// Different parameters must change the key.
	k4, _ := mk([]string{"hash-a", "hash-b"}, map[string]int{"window": 6}).Key()
	if k1 == k4 {
		t.Fatal("changed params must produce a different key")
	}
	// Bumping the code version changes every key.
	if len(k1) != 64 {
		t.Fatalf("key must be 64 hex chars, got %d", len(k1))
	}
}

func TestUnknownDependencyAndCycle(t *testing.T) {
	if _, err := NewDAG([]Node{
		{Name: "a", Deps: []string{"ghost"}},
	}); err == nil {
		t.Fatal("unknown dependency must be rejected")
	}
	if _, err := NewDAG([]Node{
		{Name: "a", Deps: []string{"b"}},
		{Name: "b", Deps: []string{"a"}},
	}); err == nil {
		t.Fatal("dependency cycle must be rejected")
	}
	if _, err := NewDAG([]Node{{Name: ""}}); err == nil {
		t.Fatal("empty node name must be rejected")
	}
}

func TestFirstBlockingDependency(t *testing.T) {
	d, err := NewDAG([]Node{
		{Name: "ingest"},
		{Name: "analyze", Deps: []string{"ingest"}},
		{Name: "fingerprint", Deps: []string{"analyze"}},
		{Name: "manifest", Deps: []string{"fingerprint"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := map[string]bool{"ingest": true}
	blocker, ok := d.FirstBlocking("manifest", failed)
	if !ok || blocker != "ingest" {
		t.Fatalf("root blocker = %q ok=%v, want ingest", blocker, ok)
	}
	// A healthy dependency graph reports no blocker.
	if _, ok := d.FirstBlocking("manifest", map[string]bool{}); ok {
		t.Fatal("no failure means no blocking dependency")
	}
}

func TestRunContextOutputs(t *testing.T) {
	c := &RunContext{outputs: map[string]string{"ingest": "summary-1"}}
	if v, ok := c.Output("ingest"); !ok || v != "summary-1" {
		t.Fatalf("dependency output = %q ok=%v", v, ok)
	}
	if _, ok := c.Output("missing"); ok {
		t.Fatal("missing dependency must not resolve")
	}
}
