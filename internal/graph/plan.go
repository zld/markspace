// Package graph performs stable dependency ordering for pipeline stages.
package graph

import (
	"sort"

	"medpipe/internal/apperr"
)

// Node is one stage and the stage IDs it directly requires.
type Node struct {
	ID       string
	Requires []string
}

// Plan is a topological schedule.  Every Levels[i] contains nodes whose
// dependencies are all in earlier levels and may therefore run in parallel.
type Plan struct {
	Order      []string
	Levels     [][]string
	requires   map[string][]string
	dependents map[string][]string
}

// Build validates and topologically sorts nodes.  Ties preserve input order,
// making the plan stable across runs and map iteration.
func Build(nodes []Node) (*Plan, error) {
	byID := make(map[string]Node, len(nodes))
	orderIndex := make(map[string]int, len(nodes))
	for i, n := range nodes {
		if n.ID == "" {
			return nil, apperr.New("E_MANIFEST_STAGE", "stage id must not be empty").WithStage("graph")
		}
		if _, dup := byID[n.ID]; dup {
			return nil, apperr.New("E_MANIFEST_STAGE", "duplicate stage %q", n.ID).
				WithStage("graph").WithField("dependencies")
		}
		byID[n.ID] = n
		orderIndex[n.ID] = i
	}

	indegree := make(map[string]int, len(nodes))
	requires := make(map[string][]string, len(nodes))
	dependents := make(map[string][]string, len(nodes))
	for id, n := range byID {
		seen := map[string]bool{}
		for _, req := range n.Requires {
			if req == id {
				return nil, apperr.New("E_CYCLE", "stage %q requires itself", id).WithStage("graph")
			}
			if _, ok := byID[req]; !ok {
				return nil, apperr.New("E_DEPENDENCY_MISSING", "stage %q requires unknown stage %q", id, req).
					WithStage("graph").WithField("dependencies")
			}
			if seen[req] {
				continue
			}
			seen[req] = true
			requires[id] = append(requires[id], req)
			dependents[req] = append(dependents[req], id)
			indegree[id]++
		}
		sort.SliceStable(requires[id], func(i, j int) bool {
			return orderIndex[requires[id][i]] < orderIndex[requires[id][j]]
		})
	}
	for id := range dependents {
		deps := dependents[id]
		sort.SliceStable(deps, func(i, j int) bool { return orderIndex[deps[i]] < orderIndex[deps[j]] })
	}

	ready := make([]string, 0)
	for _, n := range nodes {
		if indegree[n.ID] == 0 {
			ready = append(ready, n.ID)
		}
	}
	// nodes order already gives stable ready order; sort explicitly by input.
	sort.SliceStable(ready, func(i, j int) bool { return orderIndex[ready[i]] < orderIndex[ready[j]] })

	p := &Plan{requires: requires, dependents: dependents}
	for len(ready) > 0 {
		level := append([]string(nil), ready...)
		p.Levels = append(p.Levels, level)
		for _, id := range level {
			p.Order = append(p.Order, id)
			for _, next := range p.dependents[id] {
				indegree[next]--
				if indegree[next] == 0 {
					ready = append(ready, next)
				}
			}
		}
		ready = ready[:0]
		for id, deg := range indegree {
			if deg == 0 && !contains(p.Order, id) && !contains(ready, id) {
				ready = append(ready, id)
			}
		}
		sort.SliceStable(ready, func(i, j int) bool { return orderIndex[ready[i]] < orderIndex[ready[j]] })
	}

	if len(p.Order) != len(nodes) {
		remaining := make([]string, 0)
		for _, n := range nodes {
			if !contains(p.Order, n.ID) {
				remaining = append(remaining, n.ID)
			}
		}
		return nil, apperr.New("E_CYCLE", "dependency cycle involves stages %v", remaining).
			WithStage("graph").WithField("dependencies")
	}
	return p, nil
}

// SkipClosure returns failedIDs plus every stage that directly or transitively
// depends on one of them.
func (p *Plan) SkipClosure(failedIDs ...string) map[string]bool {
	skip := make(map[string]bool)
	queue := append([]string(nil), failedIDs...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if skip[id] {
			continue
		}
		skip[id] = true
		for _, dep := range p.dependents[id] {
			if !skip[dep] {
				queue = append(queue, dep)
			}
		}
	}
	return skip
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
