// Package track builds the audio/video dependency graph and normalizes
// channel layouts, sample rates and colour metadata across tracks.
package track

import (
	"sort"

	"mediamerge/internal/model"
)

// Graph is a directed track-dependency graph: an edge from A to B means
// track A depends on track B (e.g. an audio track aligned to a video
// track). Cycles are rejected.
type Graph struct {
	deps map[uint32][]uint32
	seen map[uint32]bool
}

// NewGraph returns an empty graph.
func NewGraph() *Graph {
	return &Graph{deps: make(map[uint32][]uint32), seen: make(map[uint32]bool)}
}

// AddTrack registers a track with no dependencies.
func (g *Graph) AddTrack(id uint32) {
	if !g.seen[id] {
		g.seen[id] = true
		g.deps[id] = nil
	}
}

// AddEdge records that track `from` depends on track `to`.
func (g *Graph) AddEdge(from, to uint32) {
	g.AddTrack(from)
	g.AddTrack(to)
	for _, d := range g.deps[from] {
		if d == to {
			return
		}
	}
	g.deps[from] = append(g.deps[from], to)
	sort.Slice(g.deps[from], func(i, j int) bool { return g.deps[from][i] < g.deps[from][j] })
}

// Dependencies returns the sorted direct dependencies of a track.
func (g *Graph) Dependencies(id uint32) []uint32 {
	out := append([]uint32(nil), g.deps[id]...)
	return out
}

// Validate checks for dependency cycles. On a cycle it returns a fatal
// model.Error naming the tracks involved, in deterministic order.
func (g *Graph) Validate() *model.Error {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[uint32]int)
	ids := make([]uint32, 0, len(g.seen))
	for id := range g.seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	var stack []uint32
	var visit func(id uint32) *model.Error
	visit = func(id uint32) *model.Error {
		color[id] = gray
		stack = append(stack, id)
		for _, dep := range g.deps[id] {
			switch color[dep] {
			case gray:
				return model.NewError(model.LevelFatal, "track-graph", "", -1, id,
					"dependency cycle detected: %v -> %d", stack, dep)
			case white:
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return nil
	}
	for _, id := range ids {
		if color[id] == white {
			if err := visit(id); err != nil {
				return err
			}
		}
	}
	return nil
}

// TopoOrder returns tracks ordered so dependencies come before dependents.
// Must only be called after Validate returned nil.
func (g *Graph) TopoOrder() []uint32 {
	visited := make(map[uint32]bool)
	var order []uint32
	ids := make([]uint32, 0, len(g.seen))
	for id := range g.seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var visit func(id uint32)
	visit = func(id uint32) {
		if visited[id] {
			return
		}
		visited[id] = true
		for _, dep := range g.deps[id] {
			visit(dep)
		}
		order = append(order, id)
	}
	for _, id := range ids {
		visit(id)
	}
	return order
}
