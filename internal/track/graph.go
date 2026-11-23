// Package track builds the dependency graph between declared tracks and
// derives a processing plan in topological order.  Cycles block the manifest,
// a missing optional dependency degrades that branch with a recorded reason,
// and a missing required track fails the whole manifest.
package track

import (
	"fmt"
	"sort"

	"regional.example/mediad/internal/fault"
	"regional.example/mediad/internal/manifest"
)

// NodeState is the resolution state of one declared track.
type NodeState uint8

const (
	// StatePending means the track will be processed normally.
	StatePending NodeState = iota + 1
	// StateDegraded means the track proceeds with a recorded deficiency
	// (for example, its optional dependency is absent).
	StateDegraded
	// StateSkipped means the track is not processed at all.
	StateSkipped
)

func (s NodeState) String() string {
	switch s {
	case StatePending:
		return "pending"
	case StateDegraded:
		return "degraded"
	case StateSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// Node is one track in the graph plus its resolution.
type Node struct {
	Decl  manifest.TrackDecl
	State NodeState
	// Reason explains degradation or skipping; empty for pending nodes.
	Reason string
	// MissingDep lists dependency ids that were not declared at all.
	MissingDep []uint32
}

// Graph is the validated dependency graph.
type Graph struct {
	// Nodes preserves declaration order.
	Nodes []Node
	// Edges lists dependency edges as (from=track, to=dependency).
	Edges []Edge
	// order is a stable topological order of processable tracks.
	order []uint32
}

// Edge is one dependency relation.
type Edge struct {
	From uint32
	To   uint32
}

// Build constructs the graph from a validated manifest.  It detects cycles,
// duplicate edges, self-edges and missing dependencies.  A cycle or a missing
// required track returns an unrecoverable fault; a missing optional track is
// recorded on the node and resolves it to StateDegraded/Skipped.
func Build(a *manifest.Asset) (*Graph, error) {
	g := &Graph{}
	byID := make(map[uint32]manifest.TrackDecl, len(a.Tracks))
	for _, t := range a.Tracks {
		byID[t.ID] = t
	}
	seenEdges := make(map[Edge]bool)
	for _, t := range a.Tracks {
		for _, dep := range t.DependsOn {
			e := Edge{From: t.ID, To: dep}
			if seenEdges[e] {
				// manifest validation already rejects duplicates; guard anyway.
				return nil, fault.Fatal(fault.ClassDependency, "duplicate-edge",
					fmt.Sprintf("track %d depends on %d twice", t.ID, dep))
			}
			seenEdges[e] = true
			if dep == t.ID {
				return nil, fault.Fatal(fault.ClassDependency, "self-edge",
					fmt.Sprintf("track %d depends on itself", t.ID))
			}
			g.Edges = append(g.Edges, e)
		}
	}
	if cycle := g.findCycle(); len(cycle) > 0 {
		return nil, fault.Fatal(fault.ClassDependency, "cycle",
			fmt.Sprintf("dependency cycle: %v", cycle))
	}

	// Resolve each node's state from its dependencies.
	for _, t := range a.Tracks {
		n := Node{Decl: t, State: StatePending}
		for _, dep := range t.DependsOn {
			d, ok := byID[dep]
			if !ok {
				n.MissingDep = append(n.MissingDep, dep)
				if t.Required {
					return nil, fault.Fatal(fault.ClassDependency, "missing-required-dep",
						fmt.Sprintf("track %d requires missing track %d", t.ID, dep)).OnTrack(t.ID)
				}
				continue
			}
			_ = d
		}
		g.Nodes = append(g.Nodes, n)
	}
	// Optional tracks whose own file is unsupported are skipped, and anything
	// depending on a skipped track degrades or fails.
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if len(n.MissingDep) > 0 {
			if n.Decl.Required {
				return nil, fault.Fatal(fault.ClassDependency, "missing-required-dep",
					fmt.Sprintf("track %d requires missing track %v", n.Decl.ID, n.MissingDep)).OnTrack(n.Decl.ID)
			}
			n.State = StateDegraded
			n.Reason = fmt.Sprintf("optional dependency %v is not declared", n.MissingDep)
		}
		if n.Decl.Role == manifest.RoleAudio && n.Decl.Codec != manifest.CodecPCM16 {
			if n.Decl.Required {
				return nil, fault.Fatal(fault.ClassManifest, "codec-unsupported",
					fmt.Sprintf("required audio track %d uses unsupported codec %q", n.Decl.ID, n.Decl.Codec)).OnTrack(n.Decl.ID)
			}
			n.State = StateSkipped
			n.Reason = fmt.Sprintf("unsupported codec %q", n.Decl.Codec)
		}
	}
	// Propagate: a node depending on a skipped track degrades or fails.
	changed := true
	for changed {
		changed = false
		for i := range g.Nodes {
			n := &g.Nodes[i]
			if n.State == StateSkipped {
				continue
			}
			for _, dep := range n.Decl.DependsOn {
				d, ok := byID[dep]
				if !ok {
					continue
				}
				dn := g.node(d.ID)
				if dn == nil || dn.State != StateSkipped {
					continue
				}
				if n.Decl.Required {
					return nil, fault.Fatal(fault.ClassDependency, "required-dep-skipped",
						fmt.Sprintf("track %d requires track %d, which is skipped: %s", n.Decl.ID, dep, dn.Reason)).OnTrack(n.Decl.ID)
				}
				msg := fmt.Sprintf("dependency %d is skipped: %s", dep, dn.Reason)
				if n.State != StateDegraded || n.Reason != msg {
					n.State = StateDegraded
					n.Reason = msg
					changed = true
				}
			}
		}
	}
	// Order only the processable nodes.
	g.order = g.topologicalOrder()
	return g, nil
}

func (g *Graph) node(id uint32) *Node {
	for i := range g.Nodes {
		if g.Nodes[i].Decl.ID == id {
			return &g.Nodes[i]
		}
	}
	return nil
}

// NodeByID returns the node for a track id.
func (g *Graph) NodeByID(id uint32) (*Node, bool) {
	n := g.node(id)
	return n, n != nil
}

// findCycle returns a cycle path if one exists, using an iterative DFS over
// declared tracks.  The path includes the repeated id at the end.
func (g *Graph) findCycle() []uint32 {
	adj := make(map[uint32][]uint32, len(g.Nodes)+len(g.Edges))
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	// Sort adjacency for deterministic cycle reporting.
	for k := range adj {
		sort.Slice(adj[k], func(i, j int) bool { return adj[k][i] < adj[k][j] })
	}
	ids := make([]uint32, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		ids = append(ids, n.Decl.ID)
	}
	for _, e := range g.Edges {
		// Dependencies may be undeclared; still include them as nodes so a
		// cycle through a missing id cannot hide.
		if !containsID(ids, e.From) {
			ids = append(ids, e.From)
		}
		if !containsID(ids, e.To) {
			ids = append(ids, e.To)
		}
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[uint32]int, len(ids))
	var path []uint32
	var cycle []uint32
	var visit func(id uint32) bool
	visit = func(id uint32) bool {
		color[id] = gray
		path = append(path, id)
		for _, next := range adj[id] {
			switch color[next] {
			case gray:
				// Found a back edge; extract from the first occurrence.
				start := 0
				for i, p := range path {
					if p == next {
						start = i
						break
					}
				}
				cycle = append(cycle, path[start:]...)
				cycle = append(cycle, next)
				return true
			case white:
				if visit(next) {
					return true
				}
			}
		}
		path = path[:len(path)-1]
		color[id] = black
		return false
	}
	for _, id := range ids {
		if color[id] == white {
			if visit(id) {
				return cycle
			}
		}
	}
	return nil
}

func containsID(ids []uint32, id uint32) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// topologicalOrder returns processable node ids so that every dependency
// appears before its dependents.  Ties break by ascending id so plans are
// deterministic.
func (g *Graph) topologicalOrder() []uint32 {
	byID := make(map[uint32]*Node, len(g.Nodes))
	for i := range g.Nodes {
		byID[g.Nodes[i].Decl.ID] = &g.Nodes[i]
	}
	indeg := make(map[uint32]int, len(g.Nodes))
	dependents := make(map[uint32][]uint32, len(g.Nodes))
	var processable []uint32
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.State == StateSkipped {
			continue
		}
		processable = append(processable, n.Decl.ID)
		indeg[n.Decl.ID] = 0
	}
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.State == StateSkipped {
			continue
		}
		for _, dep := range n.Decl.DependsOn {
			if d, ok := byID[dep]; ok && d.State != StateSkipped {
				indeg[n.Decl.ID]++
				dependents[dep] = append(dependents[dep], n.Decl.ID)
			}
		}
	}
	ready := make([]uint32, 0, len(processable))
	for _, id := range processable {
		if indeg[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
	var order []uint32
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		nexts := append([]uint32(nil), dependents[id]...)
		sort.Slice(nexts, func(i, j int) bool { return nexts[i] < nexts[j] })
		for _, dep := range nexts {
			indeg[dep]--
			if indeg[dep] == 0 {
				ready = append(ready, dep)
				sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
			}
		}
	}
	return order
}

// Order returns the topological processing order.
func (g *Graph) Order() []uint32 {
	out := make([]uint32, len(g.order))
	copy(out, g.order)
	return out
}

// Skipped returns nodes that will not be processed, in declaration order.
func (g *Graph) Skipped() []Node {
	var out []Node
	for _, n := range g.Nodes {
		if n.State == StateSkipped {
			out = append(out, n)
		}
	}
	return out
}

// Degraded returns degraded nodes, in declaration order.
func (g *Graph) Degraded() []Node {
	var out []Node
	for _, n := range g.Nodes {
		if n.State == StateDegraded {
			out = append(out, n)
		}
	}
	return out
}
