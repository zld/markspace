// Package dag expresses media-processing tasks as a dependency graph and
// schedules them deterministically with bounded retries and traceable
// blocking-dependency reporting.
package dag

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"mediasync/internal/clock"
)

// Func is the executable body of a node. It receives the completed upstream
// nodes so outputs can be derived deterministically from dependency results.
type Func func(n *Node, deps map[string]*Node) (any, error)

// Node is one processing step (parse, repair, recover, index, ...).
type Node struct {
	Name   string
	Deps   []string
	Params map[string]string // parameters folded into the idempotent key
	Run    Func

	// OutputFactory returns a fresh value of the node's concrete output type.
	// When set, checkpoint reuse unmarshals journal JSON into it so a reused
	// node exposes the same typed output as a freshly executed one.
	OutputFactory func() any

	// Result state, populated by the scheduler.
	Key       string
	Attempts  int
	Completed bool
	Failed    bool
	Output    any
	Err       error
}

// Graph is an immutable-once-validated set of named nodes.
type Graph struct {
	nodes map[string]*Node
	order []string // stable topological order
}

// NewGraph validates names, dependency references and acyclicity, then
// returns a graph whose iteration order is deterministic.
func NewGraph(nodes []*Node) (*Graph, error) {
	g := &Graph{nodes: make(map[string]*Node, len(nodes))}
	for _, n := range nodes {
		if n.Name == "" || n.Run == nil {
			return nil, clock.NewError(clock.ErrInvalidArgument, "node requires name and run function")
		}
		if _, dup := g.nodes[n.Name]; dup {
			return nil, clock.NewError(clock.ErrInvalidArgument, "duplicate node name %q", n.Name)
		}
		g.nodes[n.Name] = n
	}
	for _, n := range nodes {
		for _, d := range n.Deps {
			if _, ok := g.nodes[d]; !ok {
				return nil, clock.NewError(clock.ErrDependency,
					"node %q depends on unknown node %q", n.Name, d)
			}
		}
	}
	order, err := g.topo()
	if err != nil {
		return nil, err
	}
	g.order = order
	return g, nil
}

// Node returns a node by name.
func (g *Graph) Node(name string) (*Node, bool) {
	n, ok := g.nodes[name]
	return n, ok
}

// Order returns the stable topological execution order.
func (g *Graph) Order() []*Node {
	out := make([]*Node, 0, len(g.order))
	for _, name := range g.order {
		out = append(out, g.nodes[name])
	}
	return out
}

// topo performs Kahn's algorithm with lexicographic tie-breaking so the
// schedule is identical across runs and across map iteration orders.
func (g *Graph) topo() ([]string, error) {
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
	var order []string
	for len(ready) > 0 {
		sort.Strings(ready)
		name := ready[0]
		ready = ready[1:]
		order = append(order, name)
		down := append([]string(nil), adj[name]...)
		sort.Strings(down)
		for _, next := range down {
			indeg[next]--
			if indeg[next] == 0 {
				ready = append(ready, next)
			}
		}
	}
	if len(order) != len(g.nodes) {
		return nil, clock.NewError(clock.ErrDependency, "dependency cycle detected")
	}
	return order, nil
}

// IdempotentKey derives a stable SHA-256 key from the node's parameters and
// the keys of its dependencies. The same inputs and parameters always map
// to the same key, which is what makes journal reuse and retries safe.
func IdempotentKey(n *Node, depKeys map[string]string) string {
	h := sha256.New()
	h.Write([]byte(n.Name))
	h.Write([]byte{0})
	keys := make([]string, 0, len(n.Params))
	for k := range n.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte("="))
		h.Write([]byte(n.Params[k]))
		h.Write([]byte{0})
	}
	depNames := make([]string, 0, len(depKeys))
	for d := range depKeys {
		depNames = append(depNames, d)
	}
	sort.Strings(depNames)
	for _, d := range depNames {
		h.Write([]byte(d))
		h.Write([]byte(":"))
		h.Write([]byte(depKeys[d]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
