// Package index builds a dependency graph over media processing nodes and
// schedules them upstream-first. Nodes whose content digest is unchanged reuse
// their prior result; only a changed node and its downstream dependents are
// recomputed. Representation boundaries are reconciled against a shared time
// line and continuity breaks are explicitly marked.
package index

import (
	"fmt"
	"sort"

	"regress/internal/media"
)

// NodeKind tags which subsystem produces a node.
type NodeKind string

const (
	KindSubtitle NodeKind = "subtitle"
	KindRTP      NodeKind = "rtp"
	KindMP4      NodeKind = "mp4"
	KindManifest NodeKind = "manifest"
)

// Boundary is a timeline edge in the unified rational media time base.
type Boundary struct {
	NodeID string
	Start  media.Rat
	End    media.Rat
}

// Continuity marks whether adjacent representations line up on the shared
// time line.
type Continuity string

const (
	// ContinuityOK: this node's start matches the shared boundary exactly.
	ContinuityOK Continuity = "ok"
	// ContinuityBroken: a gap or overlap was detected against the shared line.
	ContinuityBroken Continuity = "broken"
)

// Node is one unit of processing with explicit upstream dependencies.
type Node struct {
	ID       string
	Kind     NodeKind
	Input    string // content digest of the node's own input
	Deps     []string
	Boundary Boundary
	// Compute runs the node. It receives the resolved result digests of its
	// dependencies and returns this node's result digest and optional payload.
	Compute func(depResults map[string]string) (resultDigest string, payload []byte, err error)
}

// NodeResult captures the outcome of scheduling one node.
type NodeResult struct {
	NodeID       string
	Kind         NodeKind
	Reused       bool // true when a valid checkpoint made recompute unnecessary
	ResultDigest string
	Payload      []byte
	Continuity   Continuity
	Boundary     Boundary
}

// Break records one continuity interruption on the shared time line.
type Break struct {
	NodeID   string
	Expected media.Rat
	Got      media.Rat
	Detail   string
}

// Graph is the nodes plus their scheduling state.
type Graph struct {
	nodes map[string]*Node
	order []string // validated topological order
}

// NewGraph validates dependencies and computes a deterministic topological
// order (ties broken by node ID so output is byte-stable across runs).
func NewGraph(nodes []*Node) (*Graph, error) {
	g := &Graph{nodes: map[string]*Node{}}
	for _, n := range nodes {
		if n == nil || n.ID == "" {
			return nil, media.InvalidInput("graph node must have an id")
		}
		if _, dup := g.nodes[n.ID]; dup {
			return nil, media.InvalidInput("duplicate graph node id %q", n.ID)
		}
		g.nodes[n.ID] = n
	}
	for _, n := range nodes {
		for _, d := range n.Deps {
			if _, ok := g.nodes[d]; !ok {
				return nil, media.InvalidInput("node %q depends on unknown node %q", n.ID, d)
			}
		}
	}
	order, err := topoSort(g.nodes)
	if err != nil {
		return nil, err
	}
	g.order = order
	return g, nil
}

// topoSort returns node IDs in dependency-first order using Kahn's algorithm
// with lexicographic tie-breaking.
func topoSort(nodes map[string]*Node) ([]string, error) {
	indeg := make(map[string]int, len(nodes))
	adj := map[string][]string{}
	for id, n := range nodes {
		indeg[id] = len(n.Deps)
		for _, d := range n.Deps {
			adj[d] = append(adj[d], id)
		}
	}
	ready := []string{}
	for id, d := range indeg {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	order := make([]string, 0, len(nodes))
	for len(ready) > 0 {
		sort.Strings(ready)
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		for _, next := range adj[id] {
			indeg[next]--
			if indeg[next] == 0 {
				ready = append(ready, next)
			}
		}
	}
	if len(order) != len(nodes) {
		return nil, media.InvalidInput("dependency graph contains a cycle")
	}
	return order, nil
}

// Order exposes the validated topological order.
func (g *Graph) Order() []string { return append([]string(nil), g.order...) }

// DepDigest builds the dependency-digest map a node's checkpoint must match.
func (g *Graph) depDigestMap(n *Node, results map[string]*NodeResult) map[string]string {
	out := make(map[string]string, len(n.Deps))
	for _, d := range n.Deps {
		out[d] = results[d].ResultDigest
	}
	return out
}

// CheckedResult is the full scheduling outcome for inspection by the driver.
type CheckedResult struct {
	Results []NodeResult
	Breaks  []Break
	// Rebuilt counts how many nodes actually recomputed (vs reused).
	Rebuilt int
}

// Checker abstracts checkpoint validation so the graph stays decoupled from
// the concrete store implementation. Lookup returns a reusable result only
// when the stored record matches the node's current input digest and all
// upstream dependency result digests; otherwise ok is false and the node is
// recomputed.
type Checker interface {
	Lookup(nodeID, inputDigest string, depDigests map[string]string) (resultDigest string, payload []byte, ok bool, err error)
}

// Run executes every node in dependency order. A node whose checkpoint
// validates is reused and skips Compute; every other node is recomputed, so a
// changed input rebuilds exactly that node and its downstream dependents.
// Boundary continuity is checked against the shared time line as nodes
// complete.
func (g *Graph) Run(checker Checker) (*CheckedResult, error) {
	results := map[string]*NodeResult{}
	out := &CheckedResult{}
	var sharedEnd media.Rat
	haveShared := false

	for _, id := range g.order {
		n := g.nodes[id]
		depMap := g.depDigestMap(n, results)

		reused := false
		var resultDigest string
		var payload []byte
		if checker != nil {
			rd, pl, ok, err := checker.Lookup(id, n.Input, depMap)
			if err != nil {
				return nil, err
			}
			if ok {
				resultDigest, payload, reused = rd, pl, true
			}
		}
		if !reused {
			if n.Compute == nil {
				return nil, media.InvalidInput("node %q has no compute function", id)
			}
			rd, pl, err := n.Compute(depMap)
			if err != nil {
				return nil, fmt.Errorf("node %s: %w", id, err)
			}
			resultDigest, payload = rd, pl
			out.Rebuilt++
		}

		// Continuity: after the first anchor, each node must begin exactly
		// where the shared time line currently ends; otherwise mark a break.
		cont := ContinuityOK
		if haveShared && n.Boundary.Start.Cmp(sharedEnd) != 0 {
			cont = ContinuityBroken
			out.Breaks = append(out.Breaks, Break{
				NodeID:   id,
				Expected: sharedEnd,
				Got:      n.Boundary.Start,
				Detail: fmt.Sprintf("start %s != shared end %s",
					n.Boundary.Start, sharedEnd),
			})
		}
		if !haveShared || n.Boundary.End.Cmp(sharedEnd) > 0 {
			sharedEnd = n.Boundary.End
			haveShared = true
		}

		res := &NodeResult{
			NodeID:       id,
			Kind:         n.Kind,
			Reused:       reused,
			ResultDigest: resultDigest,
			Payload:      payload,
			Continuity:   cont,
			Boundary:     n.Boundary,
		}
		results[id] = res
		out.Results = append(out.Results, *res)
	}
	return out, nil
}
