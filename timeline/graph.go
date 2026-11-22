package timeline

import "sort"

// Graph is the validated dependency graph between tracks. Edges point from an
// upstream dependency to the track that consumes it, so topological order is
// the legal processing order.
type Graph struct {
	edges map[string][]string // dependency -> dependents
	indeg map[string]int
	nodes map[string]struct{}
}

// BuildGraph validates that every dependency names a known track and then
// constructs the graph. A missing upstream is an error here, before any
// processing starts.
func BuildGraph(tracks []*Track) (*Graph, error) {
	g := &Graph{
		edges: make(map[string][]string),
		indeg: make(map[string]int),
		nodes: make(map[string]struct{}),
	}
	for _, t := range tracks {
		if _, dup := g.nodes[t.ID]; dup {
			return nil, NewError(ErrCodeDuplicateTimestamp, StageTimeline, t.ID,
				"duplicate track id %q", t.ID)
		}
		g.nodes[t.ID] = struct{}{}
		g.indeg[t.ID] = 0
	}
	for _, t := range tracks {
		seen := make(map[string]struct{}, len(t.DependsOn))
		for _, dep := range t.DependsOn {
			if _, ok := g.nodes[dep]; !ok {
				return nil, NewError(ErrCodeMissingBlock, StageTimeline, t.ID,
					"track %q depends on unknown upstream %q", t.ID, dep)
			}
			if dep == t.ID {
				return nil, NewError(ErrCodeCycle, StageTimeline, t.ID,
					"track %q depends on itself", t.ID)
			}
			if _, dup := seen[dep]; dup {
				continue
			}
			seen[dep] = struct{}{}
			g.edges[dep] = append(g.edges[dep], t.ID)
			g.indeg[t.ID]++
		}
	}
	return g, nil
}

// TopoSort runs Kahn's algorithm. Ties break lexicographically so the result
// is deterministic. A cycle is reported with the ids still in the queue.
func (g *Graph) TopoSort() ([]string, error) {
	indeg := make(map[string]int, len(g.indeg))
	for k, v := range g.indeg {
		indeg[k] = v
	}
	var ready []string
	for n := range g.nodes {
		if indeg[n] == 0 {
			ready = append(ready, n)
		}
	}
	order := make([]string, 0, len(g.nodes))
	for len(ready) > 0 {
		sort.Strings(ready)
		n := ready[0]
		ready = ready[1:]
		order = append(order, n)
		next := append([]string(nil), g.edges[n]...)
		sort.Strings(next)
		for _, m := range next {
			indeg[m]--
			if indeg[m] == 0 {
				ready = append(ready, m)
			}
		}
	}
	if len(order) != len(g.nodes) {
		var blocked []string
		for n, d := range indeg {
			if d > 0 {
				blocked = append(blocked, n)
			}
		}
		sort.Strings(blocked)
		return nil, NewError(ErrCodeCycle, StageTimeline, "",
			"dependency cycle involves tracks %v", blocked)
	}
	return order, nil
}

// Upstreams returns the direct dependencies of a track in stable order.
func (g *Graph) Upstreams(trackID string) []string {
	var deps []string
	for dep, ds := range g.edges {
		for _, d := range ds {
			if d == trackID {
				deps = append(deps, dep)
			}
		}
	}
	sort.Strings(deps)
	return deps
}

// Downstreams returns every track that directly depends on trackID.
func (g *Graph) Downstreams(trackID string) []string {
	out := append([]string(nil), g.edges[trackID]...)
	sort.Strings(out)
	return out
}
