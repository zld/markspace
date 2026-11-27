package mediacore

import (
	"sort"
	"strings"
)

// TrackNode is one vertex of the inter-track dependency graph.
type TrackNode struct {
	ID        uint32
	Kind      string
	DependsOn []uint32
}

// TrackGraph is a directed graph whose edges run from a track to the tracks
// it depends on. Topological order therefore yields dependencies first.
type TrackGraph struct {
	Nodes    map[uint32]*TrackNode
	offsetOf map[uint32]int64
}

// NewTrackGraph builds a graph from manifest track declarations. References to
// a track that was never declared are themselves an error.
func NewTrackGraph(decls []*TrackDecl) (*TrackGraph, error) {
	g := &TrackGraph{Nodes: map[uint32]*TrackNode{}, offsetOf: map[uint32]int64{}}
	for _, d := range decls {
		g.Nodes[d.ID] = &TrackNode{ID: d.ID, Kind: d.Kind, DependsOn: d.DependsOn}
		g.offsetOf[d.ID] = d.ByteOff
	}
	for _, d := range decls {
		for _, dep := range d.DependsOn {
			if _, ok := g.Nodes[dep]; !ok {
				return nil, WrapMsg(ErrIllegalField, d.Version, d.ID, StageGraph, d.ByteOff,
					"dependency on undeclared track "+utoa(dep))
			}
		}
	}
	return g, nil
}

// TopoSort returns track IDs with every dependency emitted before its
// dependents. Ties break by ID for determinism. A cycle yields ErrCycle whose
// message lists every track participating in the remaining SCC.
func (g *TrackGraph) TopoSort(asset string) ([]uint32, error) {
	indeg := make(map[uint32]int, len(g.Nodes))
	dependents := make(map[uint32][]uint32)
	for id, n := range g.Nodes {
		indeg[id] += 0
		for _, dep := range n.DependsOn {
			// edge dep -> id; id waits for each of its dependencies
			dependents[dep] = append(dependents[dep], id)
			indeg[id]++
		}
	}
	var ready []uint32
	for id, d := range indeg {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	var order []uint32
	for len(ready) > 0 {
		sort.Slice(ready, func(i, j int) bool { return ready[i] > ready[j] })
		cur := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		order = append(order, cur)
		for _, next := range dependents[cur] {
			indeg[next]--
			if indeg[next] == 0 {
				ready = append(ready, next)
			}
		}
	}
	if len(order) != len(g.Nodes) {
		var cyclic []uint32
		var off int64 = -1
		for id, d := range indeg {
			if d > 0 {
				cyclic = append(cyclic, id)
				if n := g.Nodes[id]; n != nil {
					if bo := g.offsetOf[id]; bo >= 0 && (off < 0 || bo < off) {
						off = bo
					}
				}
			}
		}
		sort.Slice(cyclic, func(i, j int) bool { return cyclic[i] < cyclic[j] })
		parts := make([]string, len(cyclic))
		for i, id := range cyclic {
			parts[i] = utoa(id)
		}
		e := WrapMsg(ErrCycle, asset, 0, StageGraph, off,
			"cycle involves tracks ["+strings.Join(parts, ",")+"]")
		e.Tracks = cyclic
		return nil, e
	}
	return order, nil
}

// ValidateGraph is the one-call entry used by the pipeline: build and sort in
// one step, returning the publishable order or a sourced cycle error.
func ValidateGraph(asset string, decls []*TrackDecl) ([]uint32, error) {
	g, err := NewTrackGraph(decls)
	if err != nil {
		return nil, err
	}
	return g.TopoSort(asset)
}

func utoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
