// Package track models cross-track dependency edges (tref-style references,
// hint tracks, parameter-set tracks) and produces a topological recovery
// plan. Missing dependencies and cycles never cause silent skipping: every
// stopped track carries an explicit reason.
package track

import (
	"mediacheck/internal/damage"
)

// EdgeKind is the dependency relationship between two tracks.
type EdgeKind string

const (
	// EdgeHint: a hint track references its media track.
	EdgeHint EdgeKind = "hint"
	// EdgeDescription: the target needs parameter sets / descriptions from
	// the source.
	EdgeDescription EdgeKind = "description"
	// EdgeDerived: the target is derived from the source.
	EdgeDerived EdgeKind = "derived"
	// EdgeExternal: edge supplied from the sidecar manifest rather than a
	// container box.
	EdgeExternal EdgeKind = "external"
)

// Edge is a directed dependency: TrackID depends on DependsOn.
type Edge struct {
	TrackID   uint32   `json:"track_id"`
	DependsOn uint32   `json:"depends_on"`
	Kind      EdgeKind `json:"kind"`
}

// KindFromString maps a four-character MP4 tref type (or an external label)
// onto an EdgeKind; unknown values become EdgeExternal.
func KindFromString(s string) EdgeKind {
	switch s {
	case "hint":
		return EdgeHint
	case "cdsc":
		return EdgeDescription
	case "dsub", "hind", "vdep":
		return EdgeDerived
	default:
		return EdgeExternal
	}
}

// Graph holds the tracks known to the asset and their dependency edges.
type Graph struct {
	tracks map[uint32]bool
	edges  []Edge
}

// NewGraph builds an empty graph.
func NewGraph() *Graph { return &Graph{tracks: map[uint32]bool{}} }

// AddTrack registers a track ID.
func (g *Graph) AddTrack(id uint32) { g.tracks[id] = true }

// AddEdge registers a dependency, also registering both endpoints. Use it
// for edges provenanced from container boxes where both tracks are known.
func (g *Graph) AddEdge(e Edge) {
	g.tracks[e.TrackID] = true
	g.tracks[e.DependsOn] = true
	g.edges = append(g.edges, e)
}

// AddManifestEdge registers an edge from an external manifest/sidecar: the
// dependent track is registered but the target is not, so a missing target
// surfaces as a dependency_missing stop during planning instead of being
// silently made valid.
func (g *Graph) AddManifestEdge(e Edge) {
	g.tracks[e.TrackID] = true
	g.edges = append(g.edges, e)
}

// Edges returns the registered edges in insertion order.
func (g *Graph) Edges() []Edge { return append([]Edge(nil), g.edges...) }

// Has reports whether a track is registered.
func (g *Graph) Has(id uint32) bool { return g.tracks[id] }

// Plan is the output of topological planning.
type Plan struct {
	// Order holds the tracks that can be processed, dependencies first.
	Order []uint32 `json:"order"`
	// Stopped lists tracks that must not be processed, each with the reason.
	Stopped []Stop `json:"stopped"`
	// Cycles lists the edges that participate in a dependency cycle.
	Cycles [][]Edge `json:"cycles"`
}

// Stop explains why a track is excluded from the plan.
type Stop struct {
	TrackID uint32      `json:"track_id"`
	Code    damage.Code `json:"code"`
	Detail  string      `json:"detail"`
}

// Plan computes the recovery order with Kahn's algorithm. Tracks whose
// dependency points at an unregistered track are stopped with
// dependency_missing. Tracks inside a cycle are stopped with
// dependency_cycle. The method is deterministic: ties break by ascending
// track ID and edge input order is stable.
func (g *Graph) Plan() Plan {
	indeg := make(map[uint32]int, len(g.tracks))
	adj := make(map[uint32][]Edge, len(g.tracks))
	ids := make([]uint32, 0, len(g.tracks))
	for id := range g.tracks {
		ids = append(ids, id)
		indeg[id] = 0
	}
	// Deterministic iteration order for everything that follows.
	sortUint32(ids)

	// Build the adjacency view first; dangling edges (target not in the
	// track set) mark their dependent track as blocked, and the blockage
	// propagates to every downstream track — a track whose dependency is
	// stopped is itself stopped, never silently scheduled.
	for _, e := range g.edges {
		if g.tracks[e.DependsOn] {
			adj[e.DependsOn] = append(adj[e.DependsOn], e)
		}
	}
	blocked := map[uint32]bool{}
	queue := make([]uint32, 0)
	for _, e := range g.edges {
		if !g.tracks[e.DependsOn] && !blocked[e.TrackID] {
			blocked[e.TrackID] = true
			queue = append(queue, e.TrackID)
			indeg[e.TrackID] = -1
		}
	}
	sortUint32(queue)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		next := append([]Edge(nil), adj[cur]...)
		sortEdges(next)
		for _, e := range next {
			if blocked[e.TrackID] {
				continue
			}
			blocked[e.TrackID] = true
			indeg[e.TrackID] = -1
			queue = insertSorted(queue, e.TrackID)
		}
	}
	var stopped []Stop
	for _, id := range ids {
		if !blocked[id] {
			continue
		}
		reason := "its dependency chain contains an unregistered track"
		code := damage.CodeDependencyMissing
		for _, e := range g.edges {
			if e.TrackID == id && !g.tracks[e.DependsOn] {
				reason = "depends on unregistered track " + itoa(e.DependsOn)
			}
		}
		stopped = append(stopped, Stop{TrackID: id, Code: code, Detail: reason})
	}
	for _, e := range g.edges {
		if !blocked[e.TrackID] {
			indeg[e.TrackID]++
		}
	}

	// Kahn with an ascending-ID ready queue.
	ready := make([]uint32, 0)
	for _, id := range ids {
		if !blocked[id] && indeg[id] == 0 {
			ready = append(ready, id)
		}
	}
	plan := Plan{}
	consumed := map[uint32]bool{}
	for len(ready) > 0 {
		cur := ready[0]
		ready = ready[1:]
		if consumed[cur] {
			continue
		}
		consumed[cur] = true
		plan.Order = append(plan.Order, cur)
		next := append([]Edge(nil), adj[cur]...)
		sortEdges(next)
		for _, e := range next {
			indeg[e.TrackID]--
			if indeg[e.TrackID] == 0 {
				ready = insertSorted(ready, e.TrackID)
			}
		}
	}

	// Everything not consumed is part of (or downstream of) a cycle.
	var cyclic []uint32
	for _, id := range ids {
		if !consumed[id] && !blocked[id] {
			cyclic = append(cyclic, id)
		}
	}
	if len(cyclic) > 0 {
		plan.Cycles = g.cycleEdges(cyclic)
		for _, id := range cyclic {
			stopped = append(stopped, Stop{
				TrackID: id,
				Code:    damage.CodeDependencyCycle,
				Detail:  "track participates in a dependency cycle",
			})
		}
	}
	plan.Stopped = stopped
	return plan
}

// cycleEdges returns the edges whose both endpoints are in the cyclic set.
func (g *Graph) cycleEdges(cyclic []uint32) [][]Edge {
	inSet := map[uint32]bool{}
	for _, id := range cyclic {
		inSet[id] = true
	}
	var out []Edge
	for _, e := range g.edges {
		if inSet[e.TrackID] && inSet[e.DependsOn] {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return [][]Edge{out}
}

// MarkMissingDependency stops a track when an external manifest references a
// source track absent from the container.
func (g *Graph) MarkMissingDependency(trackID, missing uint32) Stop {
	g.AddTrack(trackID)
	return Stop{
		TrackID: trackID,
		Code:    damage.CodeDependencyMissing,
		Detail:  "dependency track " + itoa(missing) + " is absent from the container",
	}
}

// sortUint32 sorts ascending; small local helper avoids a sort import in
// every function (kept in helpers.go).
func sortUint32(x []uint32) {
	for i := 1; i < len(x); i++ {
		for j := i; j > 0 && x[j-1] > x[j]; j-- {
			x[j-1], x[j] = x[j], x[j-1]
		}
	}
}

func insertSorted(x []uint32, v uint32) []uint32 {
	i := 0
	for i < len(x) && x[i] < v {
		i++
	}
	x = append(x, 0)
	copy(x[i+1:], x[i:])
	x[i] = v
	return x
}

func sortEdges(x []Edge) {
	for i := 1; i < len(x); i++ {
		for j := i; j > 0 && x[j-1].TrackID > x[j].TrackID; j-- {
			x[j-1], x[j] = x[j], x[j-1]
		}
	}
}

func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
