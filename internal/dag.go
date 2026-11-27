package rtpreplay

import (
	"fmt"
	"sort"
)

// NodeID identifies a DAG node. IDs are stable strings so a rerun can match
// prior artifacts.
type NodeID string

// Node is one unit of work in the replay pipeline. Run receives the outputs of
// its dependencies keyed by producer ID.
type Node struct {
	ID       NodeID
	Deps     []NodeID
	Run      func(deps map[NodeID]any) (any, error)
	MaxRetry int
}

// Dag is a directed acyclic graph of nodes. InputDigest identifies the run's
// root input (for rtpreplay, the validated trace) and participates in every
// node's idempotency key, so two different traces can never share a cached
// root artifact.
type Dag struct {
	nodes       map[NodeID]*Node
	order       []NodeID
	inputDigest string
}

// NewDag validates the graph up front: duplicate IDs, unknown dependencies,
// and cycles are configuration errors, never runtime surprises.
func NewDag(nodes []*Node) (*Dag, error) {
	d := &Dag{nodes: make(map[NodeID]*Node, len(nodes))}
	for _, n := range nodes {
		if n == nil || n.ID == "" {
			return nil, NewError(CodeInvalidConfig, "dag", "node has empty ID")
		}
		if n.Run == nil {
			return nil, NewError(CodeInvalidConfig, "dag", fmt.Sprintf("node %q has nil Run", n.ID))
		}
		if _, dup := d.nodes[n.ID]; dup {
			return nil, NewError(CodeInvalidConfig, "dag", fmt.Sprintf("duplicate node %q", n.ID))
		}
		d.nodes[n.ID] = n
	}
	for _, n := range nodes {
		for _, dep := range n.Deps {
			if _, ok := d.nodes[dep]; !ok {
				return nil, NewError(CodeInvalidConfig, "dag", fmt.Sprintf("node %q depends on unknown node %q", n.ID, dep))
			}
		}
	}
	order, err := d.topoSort()
	if err != nil {
		return nil, err
	}
	d.order = order
	return d, nil
}

func (d *Dag) topoSort() ([]NodeID, error) {
	indeg := make(map[NodeID]int, len(d.nodes))
	children := make(map[NodeID][]NodeID, len(d.nodes))
	for id, n := range d.nodes {
		indeg[id] = len(n.Deps)
		for _, dep := range n.Deps {
			children[dep] = append(children[dep], id)
		}
	}
	// Deterministic order: sort ready nodes lexicographically.
	ready := make([]NodeID, 0, len(d.nodes))
	for id, deg := range indeg {
		if deg == 0 {
			ready = append(ready, id)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
	var out []NodeID
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		out = append(out, id)
		next := children[id]
		sort.Slice(next, func(i, j int) bool { return next[i] < next[j] })
		for _, c := range next {
			indeg[c]--
			if indeg[c] == 0 {
				ready = append(ready, c)
			}
		}
		sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
	}
	if len(out) != len(d.nodes) {
		return nil, NewError(CodeInvalidConfig, "dag", "dependency cycle detected")
	}
	return out, nil
}

// Order returns the deterministic execution order.
func (d *Dag) Order() []NodeID { return append([]NodeID(nil), d.order...) }

// SetInputDigest records the root input digest used by idempotency keys.
func (d *Dag) SetInputDigest(digest string) { d.inputDigest = digest }

// InputDigest returns the root input digest.
func (d *Dag) InputDigest() string { return d.inputDigest }
