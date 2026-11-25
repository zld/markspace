package timeline

import (
	"sort"

	"mediapack/model"
)

// Node describes one track and the tracks that must reach a boundary first.
type Node struct {
	ID              model.TrackID
	Kind            model.UnitKind
	DependsOn       []model.TrackID
	WaitForKeyframe bool
}

// Graph is an immutable validated track dependency graph.
type Graph struct {
	nodes map[model.TrackID]Node
	order []model.TrackID
}

// NewGraph rejects missing dependencies and cycles. The returned topological
// order is deterministic: ready identifiers are visited in lexical order.
func NewGraph(nodes []Node) (*Graph, error) {
	g := &Graph{nodes: make(map[model.TrackID]Node, len(nodes))}
	for _, n := range nodes {
		if n.ID == "" {
			return nil, model.NewError("", "", -1, model.CategoryInvalidArgument, "track id is empty", nil)
		}
		if _, dup := g.nodes[n.ID]; dup {
			return nil, model.NewError(n.ID, "", -1, model.CategoryInvalidArgument, "duplicate track node", nil)
		}
		g.nodes[n.ID] = n
	}

	for _, n := range nodes {
		for _, dep := range n.DependsOn {
			if _, ok := g.nodes[dep]; !ok {
				return nil, model.NewError(n.ID, "", -1, model.CategoryMissingDependency,
					"depends on missing track "+string(dep), nil)
			}
		}
	}

	indegree := make(map[model.TrackID]int, len(nodes))
	dependents := make(map[model.TrackID][]model.TrackID, len(nodes))
	for id, n := range g.nodes {
		seen := make(map[model.TrackID]bool)
		for _, dep := range n.DependsOn {
			if dep == id {
				return nil, model.NewError(id, "", -1, model.CategoryDependencyCycle, "track depends on itself", nil)
			}
			if !seen[dep] {
				seen[dep] = true
				indegree[id]++
				dependents[dep] = append(dependents[dep], id)
			}
		}
	}

	ready := make([]model.TrackID, 0)
	for id, deg := range indegree {
		if deg == 0 {
			ready = append(ready, id)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })

	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		g.order = append(g.order, id)

		next := append([]model.TrackID(nil), dependents[id]...)
		sort.Slice(next, func(i, j int) bool { return next[i] < next[j] })
		newReady := make([]model.TrackID, 0, len(next))
		for _, dependent := range next {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				newReady = append(newReady, dependent)
			}
		}
		// Merge and sort so lexical ordering is independent of map iteration.
		ready = append(ready, newReady...)
		sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
	}

	if len(g.order) != len(nodes) {
		remaining := make([]string, 0)
		for id, deg := range indegree {
			if deg > 0 {
				remaining = append(remaining, string(id))
			}
		}
		sort.Strings(remaining)
		return nil, model.NewError("", "", -1, model.CategoryDependencyCycle,
			"dependency cycle among tracks: "+joinSorted(remaining), nil)
	}
	return g, nil
}

func joinSorted(items []string) string {
	out := ""
	for i, item := range items {
		if i > 0 {
			out += ","
		}
		out += item
	}
	return out
}

// TopoOrder returns the stable topological order.
func (g *Graph) TopoOrder() []model.TrackID {
	return append([]model.TrackID(nil), g.order...)
}

// Node returns a graph node.
func (g *Graph) Node(id model.TrackID) (Node, bool) {
	n, ok := g.nodes[id]
	return n, ok
}

// Tracks returns all track IDs in topological order.
func (g *Graph) Tracks() []model.TrackID {
	return g.TopoOrder()
}

// DependsOn reports whether id directly or transitively depends on candidate.
func (g *Graph) DependsOn(id, candidate model.TrackID) bool {
	seen := map[model.TrackID]bool{}
	var walk func(model.TrackID) bool
	walk = func(cur model.TrackID) bool {
		if cur == candidate {
			return true
		}
		if seen[cur] {
			return false
		}
		seen[cur] = true
		n, ok := g.nodes[cur]
		if !ok {
			return false
		}
		for _, dep := range n.DependsOn {
			if walk(dep) {
				return true
			}
		}
		return false
	}
	n, ok := g.nodes[id]
	if !ok {
		return false
	}
	for _, dep := range n.DependsOn {
		if walk(dep) {
			return true
		}
	}
	return false
}
