package timeline

// Graph is the explicit track dependency graph. Order holds track numbers in
// dependency-before-consumer order; consumers always come after every track
// they depend on.
type Graph struct {
	tracks map[uint32]*Track
	Order  []uint32
}

// NewGraph creates an empty graph.
func NewGraph() *Graph {
	return &Graph{tracks: make(map[uint32]*Track)}
}

// Has reports whether a track number is registered.
func (g *Graph) Has(number uint32) bool {
	_, ok := g.tracks[number]
	return ok
}

// Get returns the registered track, or nil.
func (g *Graph) Get(number uint32) *Track {
	return g.tracks[number]
}

// Count returns the number of registered tracks.
func (g *Graph) Count() int { return len(g.tracks) }

// Add registers a track. Re-adding the same number updates its declaration
// but keeps its graph position; exceeding MaxTracks is an error.
func (g *Graph) Add(t Track) *Error {
	if _, exists := g.tracks[t.Number]; exists {
		g.tracks[t.Number] = &t
		return nil
	}
	if len(g.tracks) >= MaxTracks {
		return &Error{Category: CategoryTrackLimit, TrackNumber: t.Number,
			Message: "track count exceeds MaxTracks"}
	}
	g.tracks[t.Number] = &t
	g.Order = append(g.Order, t.Number)
	return nil
}

// Resolve performs a stable topological sort so every dependency precedes
// its consumer. Unknown dependencies and cycles produce structured errors;
// the partial confirmed ordering is retained in g.Order on failure.
func (g *Graph) Resolve() *Error {
	state := make(map[uint32]uint8, len(g.Order)) // 0 unseen, 1 active, 2 done
	var resolved []uint32
	var walk func(n uint32) *Error
	walk = func(n uint32) *Error {
		switch state[n] {
		case 1:
			return &Error{Category: CategoryDependency, TrackNumber: n,
				Message: "dependency cycle detected"}
		case 2:
			return nil
		}
		tr := g.tracks[n]
		if tr == nil {
			return &Error{Category: CategoryInvalidTrack, TrackNumber: n,
				Message: "dependency references unknown track"}
		}
		state[n] = 1
		for _, dep := range tr.DependsOn {
			if err := walk(dep); err != nil {
				return err
			}
		}
		state[n] = 2
		resolved = append(resolved, n)
		return nil
	}
	// Walk in declaration order for a stable result.
	for _, n := range g.Order {
		if err := walk(n); err != nil {
			g.Order = append([]uint32(nil), resolved...)
			return err
		}
	}
	g.Order = resolved
	return nil
}

// ValidateReferences checks that every sample names a registered track.
func (g *Graph) ValidateReferences(samples []Sample) *Error {
	for i := range samples {
		if !g.Has(samples[i].TrackNumber) {
			return &Error{
				Category:    CategoryInvalidTrack,
				TrackNumber: samples[i].TrackNumber,
				Offset:      samples[i].Offset,
				TimeNs:      samples[i].StartNs,
				Message:     "sample references track not present in graph",
			}
		}
	}
	return nil
}
