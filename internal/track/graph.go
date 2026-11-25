// Package track builds deterministic processing order and metadata normalization.
package track

import (
	"fmt"
	"sort"
	"strings"

	"rtptrace/internal/manifest"
)

// State is the processing state of one track node.
type State string

const (
	StatePending   State = "pending"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateBlocked   State = "blocked"
)

// Node is a track dependency graph vertex.
type Node struct {
	TrackID   uint32   `json:"track_id"`
	Kind      string   `json:"kind"`
	DependsOn []uint32 `json:"depends_on"`
	Blocks    []uint32 `json:"blocks"`
	State     State    `json:"state"`
	Reason    string   `json:"reason,omitempty"`
}

// Graph is an immutable-after-construction dependency graph.
type Graph struct {
	Nodes map[uint32]*Node `json:"nodes"`
	Order []uint32         `json:"order"`
}

// InvalidDependencyError names an endpoint missing from the manifest.
type InvalidDependencyError struct {
	Dependency  manifest.Dependency `json:"dependency"`
	KnownTracks []uint32            `json:"known_tracks"`
}

func (e *InvalidDependencyError) Error() string {
	return fmt.Sprintf("invalid dependency track %d depends_on %d", e.Dependency.TrackID, e.Dependency.DependsOn)
}

// CycleError reports the remaining nodes and exact dependency edges in a cycle.
type CycleError struct {
	TrackIDs     []uint32              `json:"track_ids"`
	Dependencies []manifest.Dependency `json:"dependencies"`
}

func (e *CycleError) Error() string {
	parts := make([]string, 0, len(e.Dependencies))
	for _, d := range e.Dependencies {
		parts = append(parts, fmt.Sprintf("%d->%d", d.TrackID, d.DependsOn))
	}
	return "dependency cycle among tracks [" + joinIDs(e.TrackIDs) + "]: " + strings.Join(parts, ", ")
}

func joinIDs(ids []uint32) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = fmt.Sprint(id)
	}
	return strings.Join(s, ",")
}

// NewGraph validates dependencies and computes a stable topological order.
// TrackID depends on DependsOn, so DependsOn is processed first.
func NewGraph(tracks []manifest.Track, deps []manifest.Dependency) (*Graph, error) {
	g := &Graph{Nodes: make(map[uint32]*Node, len(tracks))}
	for _, t := range tracks {
		g.Nodes[t.ID] = &Node{TrackID: t.ID, Kind: string(t.Kind), State: StatePending}
	}
	for _, d := range deps {
		from, okFrom := g.Nodes[d.TrackID]
		to, okTo := g.Nodes[d.DependsOn]
		if !okFrom || !okTo {
			known := make([]uint32, 0, len(g.Nodes))
			for id := range g.Nodes {
				known = append(known, id)
			}
			sort.Slice(known, func(i, j int) bool { return known[i] < known[j] })
			return nil, &InvalidDependencyError{Dependency: d, KnownTracks: known}
		}
		// Duplicate edges do not alter indegree twice.
		if !contains(from.DependsOn, d.DependsOn) {
			from.DependsOn = append(from.DependsOn, d.DependsOn)
			to.Blocks = append(to.Blocks, d.TrackID)
		}
	}
	for _, n := range g.Nodes {
		sort.Slice(n.DependsOn, func(i, j int) bool { return n.DependsOn[i] < n.DependsOn[j] })
		sort.Slice(n.Blocks, func(i, j int) bool { return n.Blocks[i] < n.Blocks[j] })
	}
	order, err := g.topoOrder()
	if err != nil {
		return nil, err
	}
	g.Order = order
	return g, nil
}

func contains(xs []uint32, v uint32) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func (g *Graph) topoOrder() ([]uint32, error) {
	indegree := make(map[uint32]int, len(g.Nodes))
	downstream := make(map[uint32][]uint32, len(g.Nodes))
	for id, n := range g.Nodes {
		indegree[id] = len(n.DependsOn)
		for _, prereq := range n.DependsOn {
			downstream[prereq] = append(downstream[prereq], id)
		}
	}
	ready := make([]uint32, 0)
	for id, d := range indegree {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
	order := make([]uint32, 0, len(g.Nodes))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		newReady := make([]uint32, 0)
		for _, dep := range downstream[id] {
			indegree[dep]--
			if indegree[dep] == 0 {
				newReady = append(newReady, dep)
			}
		}
		ready = append(ready, newReady...)
		sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
	}
	if len(order) == len(g.Nodes) {
		return order, nil
	}
	remaining := make([]uint32, 0)
	for id, d := range indegree {
		if d > 0 {
			remaining = append(remaining, id)
		}
	}
	sort.Slice(remaining, func(i, j int) bool { return remaining[i] < remaining[j] })
	remSet := make(map[uint32]bool, len(remaining))
	for _, id := range remaining {
		remSet[id] = true
	}
	cycleDeps := make([]manifest.Dependency, 0)
	for _, id := range remaining {
		for _, prereq := range g.Nodes[id].DependsOn {
			if remSet[prereq] {
				cycleDeps = append(cycleDeps, manifest.Dependency{TrackID: id, DependsOn: prereq})
			}
		}
	}
	return nil, &CycleError{TrackIDs: remaining, Dependencies: cycleDeps}
}

// Execution records per-track processing states.
type Execution struct {
	States map[uint32]*Node `json:"states"`
}

// Run invokes process in dependency order. A failed prerequisite marks all
// transitive dependents blocked; their products must not be published.
func (g *Graph) Run(process func(trackID uint32) error) *Execution {
	ex := &Execution{States: make(map[uint32]*Node, len(g.Nodes))}
	for id, n := range g.Nodes {
		copyNode := *n
		copyNode.State = StatePending
		ex.States[id] = &copyNode
	}
	for _, id := range g.Order {
		n := ex.States[id]
		if blockedBy := g.failedPrerequisite(id, ex); blockedBy != 0 {
			n.State = StateBlocked
			n.Reason = fmt.Sprintf("blocked by failed track %d", blockedBy)
			continue
		}
		if err := process(id); err != nil {
			n.State = StateFailed
			n.Reason = err.Error()
			continue
		}
		n.State = StateSucceeded
	}
	return ex
}

func (g *Graph) failedPrerequisite(id uint32, ex *Execution) uint32 {
	var failed uint32
	for _, prereq := range g.Nodes[id].DependsOn {
		st := ex.States[prereq].State
		if st == StateFailed || st == StateBlocked {
			if failed == 0 || prereq < failed {
				failed = prereq
			}
		}
	}
	return failed
}

// PublishAllowed is true only when every node succeeded.
func (ex *Execution) PublishAllowed() bool {
	for _, n := range ex.States {
		if n.State != StateSucceeded {
			return false
		}
	}
	return true
}
