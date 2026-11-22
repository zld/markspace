// Package dag runs the archivist processing pipeline as a dependency graph
// with deterministic recovery: idempotent commit keys, bounded retries for
// transient failures, and precise first-blocker reporting for permanent ones.
package dag

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"archivist/internal/model"
)

// NodeState is the lifecycle status of a graph node.
type NodeState string

const (
	StatePending   NodeState = "pending"
	StateRunning   NodeState = "running"
	StateSucceeded NodeState = "succeeded"
	StateFailed    NodeState = "failed"
	StateBlocked   NodeState = "blocked" // upstream failed; never executed
)

// RunContext is passed to each node's Execute.
type RunContext struct {
	NodeID string
	// Results exposes successful upstream artifacts by node id.
	Results map[string]any
}

// Node is one unit of work. Execute performs the work; the runner wraps it
// with retry and idempotency logic.
type Node struct {
	ID       string
	Stage    string
	Deps     []string
	// InputDigest and ParamDigest together form the idempotency key. They
	// must capture everything that can change the node's output.
	InputDigest string
	ParamDigest string
	// Permanent marks failures that retries cannot cure. Transient failures
	// are the default and retried up to the configured limit.
	Permanent bool
	Execute   func(ctx *RunContext) error
}

// IdempotencyKey is the deterministic hash of a node's inputs and parameters.
// Re-running the same node with the same key must never re-commit an artifact.
func (n *Node) IdempotencyKey() string {
	canonical := struct {
		ID          string `json:"id"`
		InputDigest string `json:"input"`
		ParamDigest string `json:"params"`
	}{n.ID, n.InputDigest, n.ParamDigest}
	data, _ := json.Marshal(canonical)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// Graph is the validated set of nodes and their dependency edges.
type Graph struct {
	nodes map[string]*Node
	order []string // topologically sorted node ids
}

// NewGraph validates dependencies, rejects cycles and computes a stable
// topological execution order.
func NewGraph(nodes []*Node) (*Graph, error) {
	g := &Graph{nodes: map[string]*Node{}}
	for _, n := range nodes {
		if n == nil || n.ID == "" {
			return nil, model.AtTrack(model.StageDAG, model.ErrUnsupportedBox, 0, "nil or empty node")
		}
		if _, dup := g.nodes[n.ID]; dup {
			return nil, model.AtTrack(model.StageDAG, model.ErrUnsupportedBox, 0,
				fmt.Sprintf("duplicate node id %q", n.ID))
		}
		g.nodes[n.ID] = n
	}
	for _, n := range nodes {
		for _, dep := range n.Deps {
			if _, ok := g.nodes[dep]; !ok {
				return nil, model.AtTrack(model.StageDAG, model.ErrUnsupportedBox, 0,
					fmt.Sprintf("node %q depends on missing node %q", n.ID, dep))
			}
		}
	}
	order, err := g.topoSort()
	if err != nil {
		return nil, err
	}
	g.order = order
	return g, nil
}

// topoSort returns node ids with every dependency before its dependents.
// Ties break alphabetically so execution is deterministic across runs.
func (g *Graph) topoSort() ([]string, error) {
	indeg := map[string]int{}
	adj := map[string][]string{}
	ids := make([]string, 0, len(g.nodes))
	for id := range g.nodes {
		ids = append(ids, id)
		indeg[id] = 0
	}
	sort.Strings(ids)
	for _, id := range ids {
		for _, dep := range g.nodes[id].Deps {
			adj[dep] = append(adj[dep], id)
			indeg[id]++
		}
	}
	// Kahn's algorithm with a sorted ready set.
	ready := make([]string, 0)
	for _, id := range ids {
		if indeg[id] == 0 {
			ready = append(ready, id)
		}
	}
	var order []string
	for len(ready) > 0 {
		cur := ready[0]
		ready = ready[1:]
		order = append(order, cur)
		next := append([]string{}, adj[cur]...)
		sort.Strings(next)
		for _, nxt := range next {
			indeg[nxt]--
			if indeg[nxt] == 0 {
				ready = append(ready, nxt)
				sort.Strings(ready)
			}
		}
	}
	if len(order) != len(g.nodes) {
		return nil, model.AtTrack(model.StageDAG, model.ErrUnsupportedBox, 0,
			"dependency cycle detected")
	}
	return order, nil
}

// Order returns the topological node ids.
func (g *Graph) Order() []string { return append([]string{}, g.order...) }

// Node looks up a node by id.
func (g *Graph) Node(id string) *Node { return g.nodes[id] }

// Dependents returns all nodes that transitively depend on id.
func (g *Graph) Dependents(id string) []string {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(cur string) {
		for nid, n := range g.nodes {
			if seen[nid] {
				continue
			}
			for _, dep := range n.Deps {
				if dep == cur || seen[dep] {
					seen[nid] = true
					walk(nid)
					break
				}
			}
		}
	}
	walk(id)
	out := make([]string, 0, len(seen))
	for nid := range seen {
		out = append(out, nid)
	}
	sort.Strings(out)
	return out
}
