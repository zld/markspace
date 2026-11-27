// Package track builds the processing dependency graph between media tracks
// from codec and clock-slaving relationships, and produces a deterministic
// topological processing order. A cycle is reported with every track that
// participates in it and never yields a success result.
package track

import (
	"fmt"
	"sort"
	"strings"

	"medtrace/internal/mederr"
)

// Kind classifies a track by its Matroska TrackType.
type Kind uint8

const (
	KindVideo Kind = 1
	KindAudio Kind = 2
)

// Track describes one Matroska track and its declared dependencies.
type Track struct {
	ID      uint64
	UID     uint64
	Kind    Kind
	CodecID string
	// DependsOn lists track IDs this track explicitly needs (BlockGroup
	// reference blocks, alpha channels, muxing hints).
	DependsOn []uint64
	// ClockRefs lists tracks whose clock this track is slaved to.
	ClockRefs []uint64
}

// Graph is a validated directed graph: edge u -> v means u must be processed
// before v.
type Graph struct {
	tracks map[uint64]*Track
	edges  map[uint64]map[uint64]bool
}

// NewGraph constructs a graph, applying codec- and clock-based inference:
//   - every ClockRefs entry becomes a dependency;
//   - the codec registry adds known auxiliary-track dependencies (e.g. an
//     alpha-plane track codec depends on its paired primary video codec).
func NewGraph(tracks []Track) (*Graph, error) {
	g := &Graph{tracks: map[uint64]*Track{}, edges: map[uint64]map[uint64]bool{}}
	for i := range tracks {
		t := tracks[i]
		if _, dup := g.tracks[t.ID]; dup {
			return nil, mederr.New(mederr.CatInvalidInput, "duplicate track id %d", t.ID)
		}
		g.tracks[t.ID] = &t
		g.edges[t.ID] = map[uint64]bool{}
	}
	for _, t := range tracks {
		for _, dep := range append(append([]uint64(nil), t.DependsOn...), t.ClockRefs...) {
			if _, ok := g.tracks[dep]; !ok {
				return nil, mederr.New(mederr.CatInvalidInput,
					"track %d depends on unknown track %d", t.ID, dep)
			}
			if dep != t.ID {
				g.edges[dep][t.ID] = true
			}
		}
		if paired, ok := codecPair[t.CodecID]; ok {
			for _, other := range tracks {
				if other.ID != t.ID && other.Kind == KindVideo && other.CodecID == paired {
					g.edges[other.ID][t.ID] = true
				}
			}
		}
	}
	return g, nil
}

// codecPair maps an auxiliary codec to the primary codec it must follow.
var codecPair = map[string]string{
	"V_VP9_ALPHA": "V_VP9",
	"V_AV1_ALPHA": "V_AV1",
}

// CycleError carries the set of tracks participating in a dependency cycle.
type CycleError struct {
	Tracks []uint64
}

func (e *CycleError) Error() string {
	parts := make([]string, len(e.Tracks))
	for i, id := range e.Tracks {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return "dependency cycle involving tracks " + strings.Join(parts, " -> ")
}

// TopoSort returns track IDs in a deterministic processing order (Kahn's
// algorithm, smallest ready ID first). A cycle yields *mederr.Error
// CatCycle wrapping a *CycleError.
func (g *Graph) TopoSort() ([]uint64, error) {
	indeg := make(map[uint64]int, len(g.tracks))
	ready := make([]uint64, 0, len(g.tracks))
	for id := range g.tracks {
		indeg[id] = 0
	}
	for _, tos := range g.edges {
		for to := range tos {
			indeg[to]++
		}
	}
	for id, d := range indeg {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	order := make([]uint64, 0, len(g.tracks))
	for len(ready) > 0 {
		sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
		cur := ready[0]
		ready = ready[1:]
		order = append(order, cur)
		for to := range g.edges[cur] {
			indeg[to]--
			if indeg[to] == 0 {
				ready = append(ready, to)
			}
		}
	}
	if len(order) != len(g.tracks) {
		cycle := g.findCycle()
		return nil, mederr.New(mederr.CatCycle, "track dependency graph has a cycle").
			Wrap(&CycleError{Tracks: cycle})
	}
	return order, nil
}

// findCycle returns the sorted IDs of tracks that belong to any cycle, using
// iterative DFS coloring so large graphs cannot blow the goroutine stack.
func (g *Graph) findCycle() []uint64 {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[uint64]int{}
	onCycle := map[uint64]bool{}
	for start := range g.tracks {
		if color[start] != white {
			continue
		}
		type frame struct {
			id    uint64
			edges []uint64
			idx   int
		}
		stack := []frame{{id: start, edges: g.sortedOut(start)}}
		color[start] = gray
		for len(stack) > 0 {
			f := &stack[len(stack)-1]
			if f.idx >= len(f.edges) {
				color[f.id] = black
				stack = stack[:len(stack)-1]
				continue
			}
			next := f.edges[f.idx]
			f.idx++
			switch color[next] {
			case white:
				color[next] = gray
				stack = append(stack, frame{id: next, edges: g.sortedOut(next)})
			case gray:
				// Every frame from the back-edge target to the top is on
				// the cycle; gray ids appear at most once on the stack.
				inCycle := false
				for _, fr := range stack {
					if fr.id == next {
						inCycle = true
					}
					if inCycle {
						onCycle[fr.id] = true
					}
				}
			}
		}
	}
	out := make([]uint64, 0, len(onCycle))
	for id := range onCycle {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (g *Graph) sortedOut(id uint64) []uint64 {
	out := make([]uint64, 0, len(g.edges[id]))
	for to := range g.edges[id] {
		out = append(out, to)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
