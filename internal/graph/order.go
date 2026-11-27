// Package graph orders track processing by dependency. A track may depend on
// other tracks (for example a subtitle track aligned to a dialogue track).
// Cycles are detected and the participating tracks reported; a node whose
// dependencies are not satisfied never gets a processing slot.
package graph

import (
	"sort"

	"mediasync/internal/model"
)

// Node is one track vertex and the IDs it depends on.
type Node struct {
	TrackID    string
	DependsOn  []string
	HasFailure bool // a failed track still participates, flagged for callers
}

// Order is a deterministic topological ordering plus audit detail.
type Order struct {
	// Ordered holds tracks whose dependency chain is fully satisfiable.
	Ordered []string
	// Cycle lists every track that participates in a dependency cycle.
	Cycle []string
	// Unsatisfied maps a blocked track to the missing dependency ID.
	Unsatisfied map[string]string
	// Known is the set of declared track IDs.
	Known map[string]bool
}

// Plan builds a dependency graph from nodes and returns a stable topological
// order. Edges pointing at undeclared tracks make the dependent track
// unsatisfiable; cycles are reported as a whole.
func Plan(nodes []Node) *Order {
	o := &Order{
		Unsatisfied: map[string]string{},
		Known:       map[string]bool{},
	}
	indeg := make(map[string]int, len(nodes))
	adj := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		o.Known[n.TrackID] = true
		indeg[n.TrackID] = 0
	}

	// Build edges deterministically.
	for _, n := range nodes {
		for _, dep := range n.DependsOn {
			if !o.Known[dep] {
				// Keep the first missing dependency deterministically.
				if _, seen := o.Unsatisfied[n.TrackID]; !seen {
					o.Unsatisfied[n.TrackID] = dep
				}
				continue
			}
			adj[dep] = append(adj[dep], n.TrackID)
			indeg[n.TrackID]++
		}
	}
	for k := range adj {
		sort.Strings(adj[k])
	}

	// Kahn's algorithm with a sorted ready set for stable output.
	ready := make([]string, 0, len(nodes))
	for id, d := range indeg {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)

	visited := 0
	for len(ready) > 0 {
		cur := ready[0]
		ready = ready[1:]
		o.Ordered = append(o.Ordered, cur)
		visited++
		for _, next := range adj[cur] {
			indeg[next]--
			if indeg[next] == 0 {
				ready = insertSorted(ready, next)
			}
		}
	}

	if visited != len(indeg) {
		o.Cycle = collectCycle(indeg, adj)
	}

	// Any track blocked by a missing/unknown dependency is not schedulable.
	blocked := map[string]bool{}
	for id := range o.Unsatisfied {
		blocked[id] = true
	}
	for _, id := range o.Cycle {
		blocked[id] = true
		// Tracks downstream of a cycle node are transitively blocked.
		for _, down := range adj[id] {
			blocked[down] = true
		}
	}
	ordered := o.Ordered[:0]
	for _, id := range o.Ordered {
		if !blocked[id] {
			ordered = append(ordered, id)
		}
	}
	o.Ordered = ordered
	return o
}

// collectCycle returns the sorted set of vertices left with positive in-degree
// after Kahn exhausts the ready set — exactly the cycle participants plus any
// vertices only reachable through them.
func collectCycle(indeg map[string]int, adj map[string][]string) []string {
	var cyc []string
	for id, d := range indeg {
		if d > 0 {
			cyc = append(cyc, id)
		}
	}
	// Restrict to true cycle members via a reachability walk from each member.
	inCycle := map[string]bool{}
	for _, start := range cyc {
		if reaches(start, start, adj, map[string]bool{}) {
			inCycle[start] = true
		}
	}
	out := make([]string, 0, len(inCycle))
	for id := range inCycle {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// reaches reports whether a path exists from cur back to target.
func reaches(cur, target string, adj map[string][]string, seen map[string]bool) bool {
	for _, next := range adj[cur] {
		if next == target {
			return true
		}
		if seen[next] {
			continue
		}
		seen[next] = true
		if reaches(next, target, adj, seen) {
			return true
		}
	}
	return false
}

func insertSorted(s []string, v string) []string {
	i := sort.SearchStrings(s, v)
	s = append(s, "")
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

// Failure builds a model failure for a cycle, naming the involved tracks.
func (o *Order) CycleFailure() *model.Failure {
	if len(o.Cycle) == 0 {
		return nil
	}
	names := ""
	for i, id := range o.Cycle {
		if i > 0 {
			names += ","
		}
		names += id
	}
	f := model.NewTrackFailure("", model.StageGraph, model.ErrGraphCycle,
		"dependency cycle among tracks: "+names)
	return &f
}
