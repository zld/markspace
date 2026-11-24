// Package dag validates and schedules the processing dependency graph.
//
// A Graph is a set of named nodes with explicit dependencies. Construction
// rejects cycles, unknown dependency references and oversized graphs. The
// companion Scheduler runs nodes in topological order, retries only nodes that
// never completed, skips nodes whose idempotency key already has a checkpoint,
// and reports the first blocking dependency when a node cannot start.
package dag

import (
	"fmt"
	"sort"

	mpierr "mediapipeline/internal/errors"
)

// MaxNodes bounds graph size; larger graphs are rejected with CatLimit.
const MaxNodes = 256

// Node is one unit of processing.
type Node struct {
	// Name is the unique node identifier.
	Name string
	// Deps are names of nodes that must complete before this one.
	Deps []string
}

// Graph is a validated directed acyclic graph.
type Graph struct {
	nodes map[string]*Node
	order []string // topological order, deterministic (name-sorted ties)
}

// New builds and validates a graph.
func New(nodes []Node) (*Graph, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("dag: graph has no nodes")
	}
	if len(nodes) > MaxNodes {
		return nil, mpierr.New(mpierr.CatLimit, 0, "dag",
			fmt.Sprintf("node count %d exceeds %d", len(nodes), MaxNodes))
	}
	g := &Graph{nodes: make(map[string]*Node, len(nodes))}
	for i := range nodes {
		n := nodes[i]
		if n.Name == "" {
			return nil, mpierr.New(mpierr.CatDAG, 0, "dag", "node with empty name")
		}
		if _, dup := g.nodes[n.Name]; dup {
			return nil, mpierr.New(mpierr.CatDAG, 0, "dag",
				fmt.Sprintf("duplicate node %q", n.Name))
		}
		g.nodes[n.Name] = &n
	}
	for _, n := range g.nodes {
		for _, d := range n.Deps {
			if _, ok := g.nodes[d]; !ok {
				return nil, mpierr.New(mpierr.CatDAG, 0, "dag",
					fmt.Sprintf("node %q depends on unknown node %q", n.Name, d))
			}
		}
	}
	if err := g.topoSort(); err != nil {
		return nil, err
	}
	return g, nil
}

// topoSort uses Kahn's algorithm with sorted names for deterministic order.
func (g *Graph) topoSort() error {
	indeg := make(map[string]int, len(g.nodes))
	adj := make(map[string][]string, len(g.nodes))
	for name, n := range g.nodes {
		indeg[name] = len(n.Deps)
		for _, d := range n.Deps {
			adj[d] = append(adj[d], name)
		}
	}
	ready := make([]string, 0)
	for name, d := range indeg {
		if d == 0 {
			ready = append(ready, name)
		}
	}
	order := make([]string, 0, len(g.nodes))
	for len(ready) > 0 {
		sort.Strings(ready)
		name := ready[0]
		ready = ready[1:]
		order = append(order, name)
		for _, next := range adj[name] {
			indeg[next]--
			if indeg[next] == 0 {
				ready = append(ready, next)
			}
		}
	}
	if len(order) != len(g.nodes) {
		// Remaining nodes participate in a cycle; name one deterministically.
		cyclic := make([]string, 0)
		for name, d := range indeg {
			if d > 0 {
				cyclic = append(cyclic, name)
			}
		}
		sort.Strings(cyclic)
		return mpierr.New(mpierr.CatDAG, 0, "dag",
			fmt.Sprintf("cycle detected involving %q", cyclic[0]))
	}
	g.order = order
	return nil
}

// Order returns the validated topological execution order.
func (g *Graph) Order() []string {
	out := make([]string, len(g.order))
	copy(out, g.order)
	return out
}

// DepsOf returns the declared dependencies of a node.
func (g *Graph) DepsOf(name string) ([]string, error) {
	n, ok := g.nodes[name]
	if !ok {
		return nil, fmt.Errorf("dag: unknown node %q", name)
	}
	out := make([]string, len(n.Deps))
	copy(out, n.Deps)
	return out, nil
}
