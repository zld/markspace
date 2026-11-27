package timeline

import (
	"sort"

	"mediad/internal/model"
)

// TrackKind distinguishes the three node kinds that may appear in a job graph.
type TrackKind string

const (
	KindAudio    TrackKind = "audio"
	KindVideo    TrackKind = "video"
	KindManifest TrackKind = "manifest"
)

// Node is one audio track, video track or manifest deliverable.
type Node struct {
	ID   string
	Kind TrackKind
}

// Edge states that From must complete before To can run.
type Edge struct {
	From string
	To   string
}

// Graph is the validated dependency graph of a processing job. A manifest
// node must depend on every media node; unknown dependencies and cycles are
// rejected before any stage runs.
type Graph struct {
	Nodes []Node
	Edges []Edge
}

// Validate checks node kinds, edge endpoints and manifest coverage, then
// returns a stable topological ordering (Kahn's algorithm with lexicographic
// tie-breaking) in which every upstream precedes its downstream.
func (g *Graph) Validate() ([]string, error) {
	const stage = "graph"
	idx := map[string]int{}
	kinds := map[string]TrackKind{}
	for i, n := range g.Nodes {
		if n.ID == "" {
			return nil, model.ErrUsage.At("", 0, stage, 0, "empty node id")
		}
		switch n.Kind {
		case KindAudio, KindVideo, KindManifest:
		default:
			return nil, model.ErrUsage.At(n.ID, 0, stage, 0,
				"unknown node kind "+string(n.Kind))
		}
		if _, dup := idx[n.ID]; dup {
			return nil, model.ErrUsage.At(n.ID, 0, stage, 0, "duplicate node id")
		}
		idx[n.ID] = i
		kinds[n.ID] = n.Kind
	}
	indeg := map[string]int{}
	adj := map[string][]string{}
	seenEdge := map[[2]string]bool{}
	for _, e := range g.Edges {
		if _, ok := idx[e.From]; !ok {
			return nil, model.ErrGraphMissing.At(e.From, 0, stage, 0,
				"edge source node does not exist")
		}
		if _, ok := idx[e.To]; !ok {
			return nil, model.ErrGraphMissing.At(e.To, 0, stage, 0,
				"edge target node does not exist")
		}
		key := [2]string{e.From, e.To}
		if seenEdge[key] {
			continue
		}
		seenEdge[key] = true
		adj[e.From] = append(adj[e.From], e.To)
		indeg[e.To]++
	}
	// Every media node must eventually feed the manifest node.
	manifestIDs := make([]string, 0)
	for _, n := range g.Nodes {
		if n.Kind == KindManifest {
			manifestIDs = append(manifestIDs, n.ID)
		}
	}
	if len(manifestIDs) == 0 {
		return nil, model.ErrGraphMissing.At("", 0, stage, 0,
			"graph has no manifest node")
	}
	for _, n := range g.Nodes {
		if n.Kind == KindManifest {
			continue
		}
		if !g.reachesAny(n.ID, manifestIDs, adj) {
			return nil, model.ErrGraphMissing.At(n.ID, 0, stage, 0,
				"media node has no path to a manifest node")
		}
	}
	// Deterministic adjacency order for a stable topological result.
	for k := range adj {
		sort.Strings(adj[k])
	}
	ready := make([]string, 0)
	for _, n := range g.Nodes {
		if indeg[n.ID] == 0 {
			ready = append(ready, n.ID)
		}
	}
	sort.Strings(ready)
	order := make([]string, 0, len(g.Nodes))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		for _, next := range adj[id] {
			indeg[next]--
			if indeg[next] == 0 {
				ready = append(ready, next)
				sort.Strings(ready)
			}
		}
	}
	if len(order) != len(g.Nodes) {
		cycle := g.firstCycle(adj, indeg)
		return nil, model.ErrGraphCycle.At(cycle, 0, stage, 0,
			"dependency cycle reachable from node")
	}
	return order, nil
}

// reachesAny reports whether a DFS from id reaches any node in targets.
func (g *Graph) reachesAny(id string, targets []string, adj map[string][]string) bool {
	visited := map[string]bool{}
	stack := []string{id}
	isTarget := map[string]bool{}
	for _, t := range targets {
		isTarget[t] = true
	}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if isTarget[cur] {
			return true
		}
		if visited[cur] {
			continue
		}
		visited[cur] = true
		stack = append(stack, adj[cur]...)
	}
	return false
}

// firstCycle returns one node still carrying an incoming edge, which belongs
// to a cycle or a component downstream of one; used only for the error source.
func (g *Graph) firstCycle(adj map[string][]string, indeg map[string]int) string {
	candidates := make([]string, 0)
	for id, d := range indeg {
		if d > 0 {
			candidates = append(candidates, id)
		}
	}
	sort.Strings(candidates)
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}
