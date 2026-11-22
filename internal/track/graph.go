// Package track models cross-track dependencies and the input-to-output
// track mapping. Ordering is deterministic (ascending track ID inside every
// ready set) so repeated runs emit identical plans and reports.
package track

import (
	"errors"
	"fmt"
	"sort"

	"mediadist/internal/merr"
)

// Kind is the coarse media family of a track.
type Kind string

const (
	KindVideo    Kind = "video"
	KindAudio    Kind = "audio"
	KindSubtitle Kind = "subtitle"
	KindMetadata Kind = "metadata"
	KindUnknown  Kind = "unknown"
)

// Track is one input track and the tracks it depends on. A dependency means
// "this track's packaging must follow that track's", for example a metadata
// track that describes a video track, or a fragmented auxiliary track.
type Track struct {
	ID           uint32
	Kind         Kind
	Codec        string
	Dependencies []uint32
	Required     bool
}

// Graph is a directed acyclic graph of tracks. Dependencies point from a
// dependent track to the tracks it needs, and TopoSort returns dependencies
// before dependents.
type Graph struct {
	tracks map[uint32]*Track
}

// NewGraph returns an empty graph.
func NewGraph() *Graph { return &Graph{tracks: make(map[uint32]*Track)} }

// Add registers a track, copying its dependency slice. Track ID zero is
// reserved and duplicate IDs are rejected.
func (g *Graph) Add(t Track) error {
	if g.tracks == nil {
		g.tracks = make(map[uint32]*Track)
	}
	if t.ID == 0 {
		return merr.New(merr.StageTrack, "reserved_track_id", merr.Span{Stream: 0, Seq: -1, Offset: -1, TrackID: t.ID}, false,
			"track id 0 is reserved")
	}
	if _, exists := g.tracks[t.ID]; exists {
		return merr.New(merr.StageTrack, "duplicate_track", merr.Span{Stream: 0, Seq: -1, Offset: -1, TrackID: t.ID}, false,
			"track id %d already registered", t.ID)
	}
	if t.Kind == "" {
		t.Kind = KindUnknown
	}
	cp := t
	cp.Dependencies = append([]uint32(nil), t.Dependencies...)
	sort.Slice(cp.Dependencies, func(i, j int) bool { return cp.Dependencies[i] < cp.Dependencies[j] })
	// Remove self and duplicate edges so a repeated declaration is harmless.
	deps := cp.Dependencies[:0]
	var prev uint32
	for i, d := range cp.Dependencies {
		if d == cp.ID {
			continue
		}
		if i > 0 && d == prev {
			continue
		}
		deps = append(deps, d)
		prev = d
	}
	cp.Dependencies = deps
	g.tracks[cp.ID] = &cp
	return nil
}

// Len returns the number of registered tracks.
func (g *Graph) Len() int { return len(g.tracks) }

// Get returns a copy of a track.
func (g *Graph) Get(id uint32) (Track, bool) {
	t, ok := g.tracks[id]
	if !ok {
		return Track{}, false
	}
	return *t, true
}

// IDs returns all track IDs in ascending order.
func (g *Graph) IDs() []uint32 {
	ids := make([]uint32, 0, len(g.tracks))
	for id := range g.tracks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Validate checks that every declared dependency exists and returns a
// recoverable error for the first dangling edge in ascending track order.
func (g *Graph) Validate() error {
	for _, id := range g.IDs() {
		for _, dep := range g.tracks[id].Dependencies {
			if _, ok := g.tracks[dep]; !ok {
				return merr.New(merr.StageTrack, "missing_dependency",
					merr.Span{Stream: 0, Seq: -1, Offset: -1, TrackID: id}, true,
					"track %d depends on unknown track %d", id, dep)
			}
		}
	}
	return nil
}

// CycleError reports a rejected dependency cycle. Cycle always begins with the
// smallest track ID in the loop and repeats it at the end so the loop is
// readable without extra state.
type CycleError struct {
	Cycle []uint32
}

func (e *CycleError) Error() string {
	return fmt.Sprintf("track: dependency cycle among %v", e.Cycle)
}

// TopoSort returns track IDs with every dependency before its dependent.
// Ties are broken by ascending ID, which makes the order reproducible. A
// cycle returns *CycleError listing exactly the tracks in the loop.
func (g *Graph) TopoSort() ([]uint32, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	const (
		white = 0
		grey  = 1
		black = 2
	)
	state := make(map[uint32]int, len(g.tracks))
	order := make([]uint32, 0, len(g.tracks))
	stack := make([]uint32, 0, len(g.tracks))

	var visit func(id uint32) error
	visit = func(id uint32) error {
		switch state[id] {
		case black:
			return nil
		case grey:
			// Recover the loop slice from the first grey occurrence on stack.
			start := 0
			for i, v := range stack {
				if v == id {
					start = i
					break
				}
			}
			loop := append([]uint32(nil), stack[start:]...)
			loop = append(loop, id)
			return &CycleError{Cycle: loop}
		}
		state[id] = grey
		stack = append(stack, id)
		for _, dep := range g.tracks[id].Dependencies {
			if err := visit(dep); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = black
		order = append(order, id)
		return nil
	}

	for _, id := range g.IDs() {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// Stages returns the topological order together with each track's direct
// dependencies, useful to drive per-stage work in the pipeline.
func (g *Graph) Stages() ([]Track, error) {
	order, err := g.TopoSort()
	if err != nil {
		return nil, err
	}
	out := make([]Track, 0, len(order))
	for _, id := range order {
		out = append(out, *g.tracks[id])
	}
	return out, nil
}

// ReferencedBy returns the IDs of tracks that directly depend on id.
func (g *Graph) ReferencedBy(id uint32) []uint32 {
	var out []uint32
	for _, other := range g.IDs() {
		for _, dep := range g.tracks[other].Dependencies {
			if dep == id {
				out = append(out, other)
				break
			}
		}
	}
	return out
}

// ErrCycle is the sentinel used with errors.Is for cycle rejections.
var ErrCycle = errors.New("track: dependency cycle")

// IsCycle reports whether err is a CycleError.
func IsCycle(err error) bool {
	var ce *CycleError
	return errors.As(err, &ce)
}
