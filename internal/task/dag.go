// Package task schedules the recovery pipeline as a dependency DAG.
// Nodes move through pending -> running -> success/failed states; a
// failed dependency blocks every successor and the scheduler reports
// the first blocking node with its error location. Stable task keys and
// atomic artifact commits make retries idempotent.
package task

import (
	"sort"

	"archivecheck/internal/merr"
)

// Status is the node lifecycle state.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusSuccess Status = "success"
	StatusFailed  Status = "failed"
	StatusBlocked Status = "blocked"
)

// NodeFunc is the unit of work. It must be idempotent for a given key:
// repeated invocations produce the same artifact, never duplicates.
type NodeFunc func() error

// Node is one schedulable unit.
type Node struct {
	Key  string   // stable, deterministic task key
	Deps []string // keys that must succeed first
	Run  NodeFunc
}

// NodeState is the runtime state of a node.
type NodeState struct {
	Key      string      `json:"key"`
	State    Status      `json:"status"`
	Attempts int         `json:"attempts"`
	Err      *merr.Error `json:"error,omitempty"`
}

// BlockingDep describes the first dependency that blocks a node.
type BlockingDep struct {
	Node      string `json:"node"`
	BlockedBy string `json:"blocked_by"`
	Subsystem string `json:"subsystem"`
	Location  string `json:"location"`
	Message   string `json:"message"`
}

// Result is the scheduler outcome for one run.
type Result struct {
	States    map[string]*NodeState `json:"states"`
	Order     []string              `json:"execution_order"`
	Skipped   []string              `json:"skipped_successors"`
	Blocking  *BlockingDep          `json:"blocking_dependency,omitempty"`
	Executed  int                   `json:"executed_nodes"`
	Succeeded int                   `json:"succeeded_nodes"`
	Failed    int                   `json:"failed_nodes"`
}

// DAG is the validated task graph.
type DAG struct {
	nodes map[string]*Node
	order []string   // stable topological order
	waves [][]string // independent nodes per topological wave, run in parallel
}

// New validates the graph: unknown deps and cycles are rejected before
// any node can run.
func New(nodes []*Node) (*DAG, error) {
	d := &DAG{nodes: map[string]*Node{}}
	seen := map[string]bool{}
	for _, n := range nodes {
		if n.Key == "" {
			return nil, merr.New(merr.KindDAG, "task", "node", "node with empty key")
		}
		if seen[n.Key] {
			return nil, merr.New(merr.KindDAG, "task", "node:"+n.Key,
				"duplicate task key")
		}
		seen[n.Key] = true
		d.nodes[n.Key] = n
		d.order = append(d.order, n.Key)
	}
	for _, n := range nodes {
		for _, dep := range n.Deps {
			if _, ok := d.nodes[dep]; !ok {
				return nil, merr.New(merr.KindDAG, "task", "node:"+n.Key,
					"unknown dependency: "+dep)
			}
		}
	}
	if cycle := d.findCycle(); cycle != "" {
		return nil, merr.New(merr.KindDAG, "task", "cycle",
			"cyclic dependency rejected at startup: "+cycle)
	}
	order, err := d.topoOrder()
	if err != nil {
		return nil, err
	}
	d.order = order
	d.waves = d.computeWaves(order)
	return d, nil
}

// computeWaves partitions the topological order into parallel waves: a
// node joins wave (1 + max wave of its dependencies), so every node in a
// wave is independent of every other node in that wave and can run
// concurrently. Each wave is key-sorted for deterministic scheduling.
func (d *DAG) computeWaves(order []string) [][]string {
	depth := map[string]int{}
	maxDepth := 0
	for _, key := range order {
		level := 0
		for _, dep := range d.nodes[key].Deps {
			if depth[dep]+1 > level {
				level = depth[dep] + 1
			}
		}
		depth[key] = level
		if level > maxDepth {
			maxDepth = level
		}
	}
	waves := make([][]string, maxDepth+1)
	for key, level := range depth {
		waves[level] = append(waves[level], key)
	}
	for i := range waves {
		sort.Strings(waves[i])
	}
	return waves
}

// topoOrder returns a deterministic topological ordering using Kahn's
// algorithm with an alphabetically ordered ready set, so equal graphs
// always schedule identically.
func (d *DAG) topoOrder() ([]string, error) {
	indeg := map[string]int{}
	for key, n := range d.nodes {
		indeg[key] = len(n.Deps)
	}
	ready := []string{}
	for key, deg := range indeg {
		if deg == 0 {
			ready = append(ready, key)
		}
	}
	var order []string
	for len(ready) > 0 {
		sort.Strings(ready)
		key := ready[0]
		ready = ready[1:]
		order = append(order, key)
		for other, n := range d.nodes {
			for _, dep := range n.Deps {
				if dep == key {
					indeg[other]--
					if indeg[other] == 0 {
						ready = append(ready, other)
					}
				}
			}
		}
	}
	if len(order) != len(d.nodes) {
		return nil, merr.New(merr.KindDAG, "task", "cycle",
			"cyclic dependency rejected at startup")
	}
	return order, nil
}

// findCycle returns a description of the first cycle found, or "".
func (d *DAG) findCycle() string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var dfs func(key string) string
	dfs = func(key string) string {
		color[key] = gray
		stack = append(stack, key)
		for _, dep := range d.nodes[key].Deps {
			switch color[dep] {
			case gray:
				return key + " -> " + dep
			case white:
				if c := dfs(dep); c != "" {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[key] = black
		return ""
	}
	for _, key := range d.order {
		if color[key] == white {
			if c := dfs(key); c != "" {
				return c
			}
		}
	}
	return ""
}
