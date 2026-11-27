// Package graph defines the explicit track-processing dependency graph.
// Tracks declare which other tracks must be processed first. The graph
// produces one deterministic topological order (ties broken by track ID)
// and rejects dependency cycles outright so that no partial merge result
// is ever published from an invalid plan.
package graph

import (
	"sort"
	"strings"

	"replay/internal/fault"
)

// Track is one node in the processing graph.
type Track struct {
	// ID is the stable logical track identifier.
	ID string
	// ClockRate is the RTP timestamp frequency in hertz.
	ClockRate uint32
	// DependsOn lists track IDs that must complete before this track.
	DependsOn []string
	// Version optionally pins the asset version mapped for this track.
	Version string
	// InputOrder is the order the track appeared in the input, used to
	// prove that ordering never depends on map iteration.
	InputOrder int
}

// Graph is an immutable set of tracks.
type Graph struct {
	tracks map[string]*Track
	order  []string
}

// New validates tracks and builds a graph. Duplicate IDs, unknown
// dependencies, self-dependencies and cycles are rejected.
func New(tracks []Track) (*Graph, error) {
	g := &Graph{tracks: make(map[string]*Track, len(tracks))}
	for i := range tracks {
		t := tracks[i].clone()
		if err := validateTrack(t); err != nil {
			return nil, err
		}
		if _, exists := g.tracks[t.ID]; exists {
			return nil, fault.New(fault.KindGraph, "duplicate track ID").
				At("graph").WithTrack(t.ID)
		}
		g.tracks[t.ID] = t
		g.order = append(g.order, t.ID)
	}
	// Deterministic canonical order regardless of input order.
	sort.Strings(g.order)

	for _, id := range g.order {
		t := g.tracks[id]
		for _, dep := range t.DependsOn {
			if _, ok := g.tracks[dep]; !ok {
				return nil, fault.New(fault.KindGraph, "unknown dependency").
					At("graph").WithTrack(id)
			}
		}
	}

	if _, err := g.TopologicalOrder(); err != nil {
		return nil, err
	}
	return g, nil
}

func validateTrack(t *Track) error {
	if strings.TrimSpace(t.ID) == "" {
		return fault.New(fault.KindGraph, "track requires a non-empty ID").At("graph")
	}
	if t.ClockRate == 0 {
		return fault.New(fault.KindGraph, "track requires a positive clock rate").
			At("graph").WithTrack(t.ID)
	}
	for _, dep := range t.DependsOn {
		if dep == t.ID {
			return fault.New(fault.KindGraph, "track depends on itself").
				At("graph").WithTrack(t.ID)
		}
	}
	return nil
}

func (t Track) clone() *Track {
	c := t
	c.DependsOn = append([]string(nil), t.DependsOn...)
	sort.Strings(c.DependsOn)
	return &c
}

// Track returns a copy of one track.
func (g *Graph) Track(id string) (Track, bool) {
	t, ok := g.tracks[id]
	if !ok {
		return Track{}, false
	}
	return *t.clone(), true
}

// Len returns the node count.
func (g *Graph) Len() int { return len(g.tracks) }

// IDs returns every track ID in canonical (sorted) order.
func (g *Graph) IDs() []string {
	return append([]string(nil), g.order...)
}

// TopologicalOrder returns a stable order in which every track appears
// after its dependencies. Among ready tracks, the lexicographically
// smallest ID is chosen first, so identical graphs always yield the same
// plan.
func (g *Graph) TopologicalOrder() ([]string, error) {
	indegree := make(map[string]int, len(g.tracks))
	dependents := make(map[string][]string, len(g.tracks))
	for id, t := range g.tracks {
		indegree[id] = len(t.DependsOn)
		for _, dep := range t.DependsOn {
			dependents[dep] = append(dependents[dep], id)
		}
	}
	for _, ids := range dependents {
		sort.Strings(ids)
	}

	ready := make([]string, 0, len(g.tracks))
	for _, id := range g.order {
		if indegree[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)

	result := make([]string, 0, len(g.tracks))
	for len(ready) > 0 {
		next := ready[0]
		ready = ready[1:]
		result = append(result, next)
		for _, child := range dependents[next] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
				sort.Strings(ready)
			}
		}
	}

	if len(result) != len(g.tracks) {
		path := g.findCycle(indegree)
		return nil, fault.New(fault.KindGraph, "dependency cycle "+strings.Join(path, " -> ")).
			At("graph").WithTrack(path[0])
	}
	return result, nil
}

// findCycle returns the node path of one cycle among the stuck nodes,
// starting and ending at the same ID. It walks still-blocked nodes in
// sorted order so the reported cycle is deterministic.
func (g *Graph) findCycle(indegree map[string]int) []string {
	remaining := make([]string, 0, len(g.tracks))
	for id, deg := range indegree {
		if deg > 0 {
			remaining = append(remaining, id)
		}
	}
	sort.Strings(remaining)
	stuck := make(map[string]bool, len(remaining))
	for _, id := range remaining {
		stuck[id] = true
	}

	// Iterative DFS over edges to dependencies, restricted to stuck
	// nodes, tracking the current path to detect a back edge.
	for _, start := range remaining {
		path := []string{start}
		onPath := map[string]bool{start: true}
		if cycle := g.dfsCycle(start, stuck, path, onPath); cycle != nil {
			return cycle
		}
	}
	return []string{remaining[0]}
}

func (g *Graph) dfsCycle(node string, stuck map[string]bool, path []string, onPath map[string]bool) []string {
	t := g.tracks[node]
	for _, dep := range t.DependsOn {
		if !stuck[dep] {
			continue
		}
		if onPath[dep] {
			// Trim the path to where the cycle begins.
			for i, id := range path {
				if id == dep {
					cycle := append([]string(nil), path[i:]...)
					return append(cycle, dep)
				}
			}
			return append(append([]string(nil), path...), dep)
		}
		path = append(path, dep)
		onPath[dep] = true
		if cycle := g.dfsCycle(dep, stuck, path, onPath); cycle != nil {
			return cycle
		}
		path = path[:len(path)-1]
		delete(onPath, dep)
	}
	return nil
}

// DependsOn returns the recorded dependencies of a track.
func (g *Graph) DependsOn(id string) []string {
	t, ok := g.tracks[id]
	if !ok {
		return nil
	}
	return append([]string(nil), t.DependsOn...)
}

// Downstream returns every track that transitively depends on id,
// including id itself, in canonical order.
func (g *Graph) Downstream(id string) []string {
	if _, ok := g.tracks[id]; !ok {
		return nil
	}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(cur string) {
		if seen[cur] {
			return
		}
		seen[cur] = true
		for child, t := range g.tracks {
			for _, dep := range t.DependsOn {
				if dep == cur {
					visit(child)
				}
			}
		}
	}
	visit(id)
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
