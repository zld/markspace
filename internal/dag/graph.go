// Package dag runs the processing graph: stable topological ordering,
// cycle rejection, bounded concurrency, temporary outputs with atomic
// commit, and deterministic node keys derived from input/config digests.
package dag

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"pwf/internal/media"
)

// Version is mixed into every node key so a code change invalidates
// cached results. Bump when node semantics change.
const Version = "pwf-node-v1"

// Status is the lifecycle state of a node.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusBlocked   Status = "blocked"
)

// NodeFunc executes one node and returns the bytes it wants committed as
// its output artifact.
type NodeFunc func(ctx *RunContext) ([]byte, error)

// Node is one unit of work with explicit dependency edges.
type Node struct {
	Key          string
	InputDigest  string // digest of the node's source inputs
	ConfigDigest string // digest of the node's configuration
	Deps         []string
	Run          NodeFunc
}

// NodeKey derives the stable content-addressed key required by the
// checkpoint design: input summary + config summary + version.
func NodeKey(inputDigest, configDigest string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s", inputDigest, configDigest, Version)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Graph is the validated set of nodes.
type Graph struct {
	nodes map[string]*Node
	order []string // stable topological order
}

// NewGraph validates edges, rejects unknown dependencies and cycles, and
// fixes the deterministic execution order.
func NewGraph(nodes []Node) (*Graph, error) {
	g := &Graph{nodes: map[string]*Node{}}
	for i := range nodes {
		n := nodes[i]
		if n.Key == "" {
			return nil, media.NewError(media.KindDAGCycle, media.StageSchedule,
				"node with empty key", nil)
		}
		if _, dup := g.nodes[n.Key]; dup {
			return nil, media.NewError(media.KindDAGCycle, media.StageSchedule,
				"duplicate node key "+n.Key, nil)
		}
		g.nodes[n.Key] = &n
	}
	for _, n := range g.nodes {
		for _, d := range n.Deps {
			if _, ok := g.nodes[d]; !ok {
				return nil, media.NewError(media.KindDAGCycle, media.StageSchedule,
					fmt.Sprintf("node %s depends on unknown node %s", n.Key, d), nil)
			}
		}
	}
	order, err := g.topoSort()
	if err != nil {
		return nil, err
	}
	g.order = order
	return g, nil
}

// topoSort uses Kahn's algorithm with lexicographic tie-breaking so the
// order is stable across runs and machines.
func (g *Graph) topoSort() ([]string, error) {
	indeg := map[string]int{}
	children := map[string][]string{}
	keys := make([]string, 0, len(g.nodes))
	for k, n := range g.nodes {
		keys = append(keys, k)
		indeg[k] = len(n.Deps)
		for _, d := range n.Deps {
			children[d] = append(children[d], k)
		}
	}
	sort.Strings(keys)
	// Seed with ready keys in sorted order.
	ready := make([]string, 0)
	for _, k := range keys {
		if indeg[k] == 0 {
			ready = append(ready, k)
		}
	}
	order := make([]string, 0, len(g.nodes))
	for len(ready) > 0 {
		k := ready[0]
		ready = ready[1:]
		order = append(order, k)
		nexts := append([]string(nil), children[k]...)
		sort.Strings(nexts)
		for _, c := range nexts {
			indeg[c]--
			if indeg[c] == 0 {
				ready = insertSorted(ready, c)
			}
		}
	}
	if len(order) != len(g.nodes) {
		cycle := findCycle(g.nodes)
		return nil, media.NewError(media.KindDAGCycle, media.StageSchedule,
			"dependency cycle detected involving: "+strings.Join(cycle, " -> "), nil)
	}
	return order, nil
}

func insertSorted(s []string, v string) []string {
	i := sort.SearchStrings(s, v)
	s = append(s, "")
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

// findCycle returns one concrete cycle for the error message via DFS.
func findCycle(nodes map[string]*Node) []string {
	const (
		white, gray, black = 0, 1, 2
	)
	color := map[string]int{}
	var stack []string
	var dfs func(k string) []string
	dfs = func(k string) []string {
		color[k] = gray
		stack = append(stack, k)
		deps := append([]string(nil), nodes[k].Deps...)
		sort.Strings(deps)
		for _, d := range deps {
			if color[d] == gray {
				i := 0
				for stack[i] != d {
					i++
				}
				return append(stack[i:], d)
			}
			if color[d] == white {
				if cyc := dfs(d); cyc != nil {
					return cyc
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[k] = black
		return nil
	}
	keys := make([]string, 0, len(nodes))
	for k := range nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if cyc := dfs(k); cyc != nil {
			return cyc
		}
	}
	return []string{"unknown"}
}

// Order exposes the stable topological order.
func (g *Graph) Order() []string { return append([]string(nil), g.order...) }

// Node returns the node stored under key, or nil when absent.
func (g *Graph) Node(key string) *Node { return g.nodes[key] }
