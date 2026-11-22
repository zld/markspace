// Package graph builds the explicit dependency graph that orders every
// processing step. Nothing runs until the graph is validated, so a cycle or a
// missing required node is reported before any audio is touched.
package graph

import (
	"fmt"
	"sort"
	"strings"

	"mediacore/internal/core"
)

// NodeID names one processing step. IDs are stable strings so they can appear
// verbatim in errors and manifests.
type NodeID string

// The pipeline's canonical node IDs.
const (
	NodeAudioTrack   NodeID = "audio-track"
	NodeTimeline     NodeID = "timeline"
	NodeDecodeParams NodeID = "decode-params"
	NodeResample     NodeID = "resample"
	NodeLayout       NodeID = "channel-layout"
	NodeLoudness     NodeID = "loudness"
	NodePacketize    NodeID = "rtp-packetize"
)

// Node is one step with its declared dependencies and the parameters it
// conflicts on.
type Node struct {
	ID     NodeID
	Deps   []NodeID
	Params map[string]string
}

// Graph is a validated, ordered set of nodes.
type Graph struct {
	nodes map[NodeID]*Node
	order []NodeID
}

// New validates the node set and produces a deterministic topological order.
// Ties are broken by NodeID so identical input always yields identical order.
func New(nodes []Node) (*Graph, error) {
	g := &Graph{nodes: make(map[NodeID]*Node, len(nodes))}
	for i := range nodes {
		n := nodes[i]
		if n.ID == "" {
			return nil, core.New(core.CodeParamConflict, core.StageGraph, false, "node with empty id")
		}
		if _, dup := g.nodes[n.ID]; dup {
			return nil, core.New(core.CodeParamConflict, core.StageGraph, false,
				"duplicate node").WithNode(string(n.ID))
		}
		c := n
		g.nodes[n.ID] = &c
	}
	for _, id := range g.sortedIDs() {
		for _, dep := range g.nodes[id].Deps {
			if _, ok := g.nodes[dep]; !ok {
				return nil, core.New(core.CodeMissingTrack, core.StageGraph, false,
					fmt.Sprintf("node %q requires missing node %q", id, dep)).WithNode(string(id))
			}
		}
	}
	if err := g.checkConflicts(); err != nil {
		return nil, err
	}
	order, err := g.topoSort()
	if err != nil {
		return nil, err
	}
	g.order = order
	return g, nil
}

func (g *Graph) sortedIDs() []NodeID {
	ids := make([]NodeID, 0, len(g.nodes))
	for id := range g.nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// checkConflicts rejects two nodes that declare the same parameter with
// different values; the message names both nodes and the parameter.
func (g *Graph) checkConflicts() error {
	type owner struct {
		id    NodeID
		value string
	}
	owners := make(map[string]owner)
	for _, id := range g.sortedIDs() {
		n := g.nodes[id]
		keys := make([]string, 0, len(n.Params))
		for k := range n.Params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := n.Params[k]
			if prev, ok := owners[k]; ok && prev.value != v {
				return core.New(core.CodeParamConflict, core.StageGraph, false,
					fmt.Sprintf("parameter %q set to %q by %s and %q by %s", k, prev.value, prev.id, v, id)).
					WithNode(string(id))
			}
			if _, ok := owners[k]; !ok {
				owners[k] = owner{id: id, value: v}
			}
		}
	}
	return nil
}

// topoSort implements Kahn's algorithm with a sorted ready-set. When a cycle
// prevents progress it extracts the concrete cycle path for the error.
func (g *Graph) topoSort() ([]NodeID, error) {
	indeg := make(map[NodeID]int, len(g.nodes))
	dependents := make(map[NodeID][]NodeID, len(g.nodes))
	for id, n := range g.nodes {
		indeg[id] = len(n.Deps)
		for _, dep := range n.Deps {
			dependents[dep] = append(dependents[dep], id)
		}
	}
	var ready []NodeID
	for id, d := range indeg {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })

	order := make([]NodeID, 0, len(g.nodes))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		next := append([]NodeID(nil), dependents[id]...)
		sort.Slice(next, func(i, j int) bool { return next[i] < next[j] })
		for _, d := range next {
			indeg[d]--
			if indeg[d] == 0 {
				ready = append(ready, d)
				sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
			}
		}
	}
	if len(order) != len(g.nodes) {
		cycle := g.findCycle(indeg)
		return nil, core.New(core.CodeDependencyCycle, core.StageGraph, false,
			"dependency cycle: "+cycle).WithNode(strings.Split(cycle, " -> ")[0])
	}
	return order, nil
}

// findCycle walks unresolved edges depth-first and returns the cycle as a
// printable path such as "a -> b -> a".
func (g *Graph) findCycle(indeg map[NodeID]int) string {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := make(map[NodeID]int, len(g.nodes))
	var stack []NodeID
	var cycle []NodeID

	var visit func(NodeID) bool
	visit = func(id NodeID) bool {
		color[id] = grey
		stack = append(stack, id)
		deps := append([]NodeID(nil), g.nodes[id].Deps...)
		sort.Slice(deps, func(i, j int) bool { return deps[i] < deps[j] })
		for _, dep := range deps {
			switch color[dep] {
			case grey:
				start := 0
				for i, s := range stack {
					if s == dep {
						start = i
						break
					}
				}
				cycle = append(append([]NodeID(nil), stack[start:]...), dep)
				return true
			case white:
				if visit(dep) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return false
	}
	for _, id := range g.sortedIDs() {
		if indeg[id] > 0 && color[id] == white {
			if visit(id) {
				break
			}
		}
	}
	if len(cycle) == 0 {
		return "(unresolved)"
	}
	parts := make([]string, len(cycle))
	for i, c := range cycle {
		parts[i] = string(c)
	}
	return strings.Join(parts, " -> ")
}

// Order returns the validated processing order.
func (g *Graph) Order() []NodeID { return append([]NodeID(nil), g.order...) }

// Has reports whether a node is present.
func (g *Graph) Has(id NodeID) bool { _, ok := g.nodes[id]; return ok }

// Node returns a node by id.
func (g *Graph) Node(id NodeID) (Node, bool) {
	n, ok := g.nodes[id]
	if !ok {
		return Node{}, false
	}
	return *n, true
}

// Dependents returns the direct dependents of a node, sorted.
func (g *Graph) Dependents(id NodeID) []NodeID {
	var out []NodeID
	for _, other := range g.sortedIDs() {
		for _, dep := range g.nodes[other].Deps {
			if dep == id {
				out = append(out, other)
			}
		}
	}
	return out
}
