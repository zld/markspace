package timeline

import (
	"sort"
	"strings"

	"mediapack/merr"
)

// TrackKind classifies tracks in the dependency graph.
type TrackKind string

const (
	KindVideo    TrackKind = "video"
	KindAudio    TrackKind = "audio"
	KindRTPVideo TrackKind = "rtp-video"
	KindOutput   TrackKind = "output"
)

// Track is a node in the directed acyclic track graph.
type Track struct {
	ID            string
	Kind          TrackKind
	Source        string   // input asset path ("" for synthetic/output tracks)
	DependsOn     []string // track IDs that must be processed first
	StartNanos    int64    // placement on the shared timeline (may be non-zero)
	IntervalNanos int64    // nominal sample interval
}

// Graph is a directed acyclic graph of tracks.
type Graph struct {
	tracks map[string]*Track
}

// NewGraph returns an empty graph.
func NewGraph() *Graph { return &Graph{tracks: map[string]*Track{}} }

// Add registers a track; duplicate IDs are rejected with stage+track context.
func (g *Graph) Add(t *Track) error {
	if t.ID == "" {
		return merr.Paramf("graph", "track with empty ID")
	}
	if _, dup := g.tracks[t.ID]; dup {
		return merr.Corruptf("graph", t.ID, "duplicate track ID")
	}
	g.tracks[t.ID] = t
	return nil
}

// Get returns a track by ID.
func (g *Graph) Get(id string) (*Track, bool) {
	t, ok := g.tracks[id]
	return t, ok
}

// Tracks returns all tracks sorted by ID (deterministic).
func (g *Graph) Tracks() []*Track {
	ids := make([]string, 0, len(g.tracks))
	for id := range g.tracks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*Track, 0, len(ids))
	for _, id := range ids {
		out = append(out, g.tracks[id])
	}
	return out
}

// Validate checks that every dependency reference resolves.
func (g *Graph) Validate() error {
	for _, t := range g.Tracks() {
		for _, dep := range t.DependsOn {
			if dep == t.ID {
				return merr.Corruptf("graph", t.ID, "track depends on itself")
			}
			if _, ok := g.tracks[dep]; !ok {
				return merr.Corruptf("graph", t.ID, "missing dependency %q", dep)
			}
		}
	}
	return nil
}

// Order returns track IDs in dependency (topological) order. Cycles are
// rejected with an error naming the stage and the tracks involved.
func (g *Graph) Order() ([]string, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	indeg := map[string]int{}
	dependents := map[string][]string{}
	for _, t := range g.Tracks() {
		indeg[t.ID] = len(t.DependsOn)
		for _, dep := range t.DependsOn {
			dependents[dep] = append(dependents[dep], t.ID)
		}
	}
	var ready []string
	for id, d := range indeg {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	order := make([]string, 0, len(indeg))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		next := dependents[id]
		sort.Strings(next)
		for _, m := range next {
			indeg[m]--
			if indeg[m] == 0 {
				// insert keeping ready sorted for determinism
				i := sort.SearchStrings(ready, m)
				ready = append(ready, "")
				copy(ready[i+1:], ready[i:])
				ready[i] = m
			}
		}
	}
	if len(order) != len(indeg) {
		var cyc []string
		for id, d := range indeg {
			if d > 0 {
				cyc = append(cyc, id)
			}
		}
		sort.Strings(cyc)
		return nil, merr.Corruptf("graph", strings.Join(cyc, ","), "cyclic dependency detected")
	}
	return order, nil
}
