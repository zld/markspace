// Package graph builds the explicit stage dependency graph and produces a
// deterministic topological schedule. Every failure mode maps to a classified
// model error so the engine can report a locate-able problem and decide which
// downstream stages must be blocked.
package graph

import (
	"sort"
	"strings"

	"mediapipe/internal/model"
)

// Node is one processing stage. Deps lists node ids that must complete
// successfully before this node may run.
type Node struct {
	ID      string   `json:"id"`
	TrackID string   `json:"track_id"`
	Stage   string   `json:"stage"`
	Deps    []string `json:"deps"`
}

// Plan is a validated graph with its cached topological order.
type Plan struct {
	nodes map[string]*Node
	order []string
	// dependents[x] = nodes that directly declare x as a dependency.
	dependents map[string][]string
}

// Build validates the node set and computes the schedule. It rejects
// duplicate node ids, dependencies on unknown nodes and cycles, each as a
// classified model.MediaError.
func Build(nodes []Node) (*Plan, error) {
	p := &Plan{
		nodes:      map[string]*Node{},
		dependents: map[string][]string{},
	}
	for i := range nodes {
		n := nodes[i]
		if n.ID == "" {
			return nil, model.NewError(model.ErrInvalidConfig, "", 0, 0, "node with empty id")
		}
		if _, dup := p.nodes[n.ID]; dup {
			return nil, model.NewError(model.ErrDuplicateNode, n.TrackID, 0, 0, "node id "+n.ID)
		}
		p.nodes[n.ID] = &n
	}
	for id, n := range p.nodes {
		seen := map[string]bool{}
		for _, d := range n.Deps {
			if _, ok := p.nodes[d]; !ok {
				return nil, model.NewError(model.ErrMissingDep, n.TrackID, 0, 0,
					"node "+id+" depends on unknown node "+d)
			}
			if seen[d] {
				return nil, model.NewError(model.ErrInvalidConfig, n.TrackID, 0, 0,
					"node "+id+" repeats dependency "+d)
			}
			seen[d] = true
			p.dependents[d] = append(p.dependents[d], id)
		}
	}
	for id := range p.dependents {
		sort.Strings(p.dependents[id])
	}
	order, err := p.topoSort()
	if err != nil {
		return nil, err
	}
	p.order = order
	return p, nil
}

// topoSort runs Kahn's algorithm with a sorted ready set so reruns and
// different input orderings always yield the same schedule.
func (p *Plan) topoSort() ([]string, error) {
	indeg := make(map[string]int, len(p.nodes))
	ready := make([]string, 0)
	for id, n := range p.nodes {
		indeg[id] = len(n.Deps)
		if indeg[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	order := make([]string, 0, len(p.nodes))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		for _, dep := range p.dependents[id] {
			indeg[dep]--
			if indeg[dep] == 0 {
				ready = insertSorted(ready, dep)
			}
		}
	}
	if len(order) != len(p.nodes) {
		return nil, model.NewError(model.ErrGraphCycle, "", 0, 0,
			"cycle involves: "+strings.Join(p.stuckNodes(indeg), ", "))
	}
	return order, nil
}

func (p *Plan) stuckNodes(indeg map[string]int) []string {
	out := make([]string, 0)
	for id, d := range indeg {
		if d > 0 {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func insertSorted(s []string, v string) []string {
	i := sort.SearchStrings(s, v)
	s = append(s, "")
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

// Order returns the deterministic topological node ordering.
func (p *Plan) Order() []string { return append([]string(nil), p.order...) }

// Node looks up a node by id.
func (p *Plan) Node(id string) (*Node, bool) {
	n, ok := p.nodes[id]
	return n, ok
}

// Downstream returns the sorted, transitive set of nodes that depend on id
// directly or indirectly — the stages that a failure of id must block.
func (p *Plan) Downstream(id string) []string {
	blocked := map[string]bool{}
	queue := []string{id}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range p.dependents[cur] {
			if !blocked[next] {
				blocked[next] = true
				queue = append(queue, next)
			}
		}
	}
	out := make([]string, 0, len(blocked))
	for id := range blocked {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Schedule classifies every node against the sets of finished and failed
// node ids: runnable nodes have all dependencies finished; blocked nodes
// transitively depend on a failure. It is deterministic and side-effect free.
func (p *Plan) Schedule(finished, failed map[string]bool) (runnable, blocked []string) {
	blockedSet := map[string]bool{}
	for f := range failed {
		for _, id := range p.Downstream(f) {
			blockedSet[id] = true
		}
	}
	for _, id := range p.order {
		switch {
		case finished[id] || failed[id]:
			continue
		case blockedSet[id]:
			blocked = append(blocked, id)
		default:
			runnable = append(runnable, id)
		}
	}
	return runnable, blocked
}
