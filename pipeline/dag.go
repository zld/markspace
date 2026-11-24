// Package pipeline schedules the processing nodes as a DAG and makes every
// node idempotent. A node's identity is a digest of its inputs' summaries,
// its parameters and a fixed code version, so rerunning the same work reuses
// the checkpointed result and changed inputs never reuse a stale one.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// CodeVersion is bumped when a node's computation semantics change, forcing
// every affected idempotency key to change and old checkpoints to be ignored.
const CodeVersion = "mediad-1"

// Node is one unit of work in the DAG.
type Node struct {
	Name string
	// Deps are names of nodes that must complete successfully first.
	Deps []string
	// Inputs are content summaries of every input the node reads.
	Inputs []string
	// Params is the canonical, JSON-serialisable parameter set.
	Params any
	// Run performs the work and returns the node's output summary.
	Run func(ctx *RunContext) (output string, err error)
}

// RunContext gives a node its resolved dependency outputs during execution.
type RunContext struct {
	Node    *Node
	outputs map[string]string
	// offsets is optional provenance the node records into its checkpoint.
	offsets []SourceOffset
}

// SetSourceOffsets lets a node record the input byte ranges it consumed.
func (c *RunContext) SetSourceOffsets(offsets []SourceOffset) {
	c.offsets = append([]SourceOffset{}, offsets...)
}

// Output returns the recorded output summary of a dependency by name.
func (c *RunContext) Output(dep string) (string, bool) {
	v, ok := c.outputs[dep]
	return v, ok
}

// Key returns the deterministic idempotency key for the node.
func (n *Node) Key() (string, error) {
	canonical := struct {
		Version string   `json:"version"`
		Name    string   `json:"name"`
		Inputs  []string `json:"inputs"`
		Params  any      `json:"params"`
	}{
		Version: CodeVersion,
		Name:    n.Name,
		Inputs:  append([]string{}, n.Inputs...),
		Params:  n.Params,
	}
	sort.Strings(canonical.Inputs)
	b, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("pipeline: cannot key node %q: %w", n.Name, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// DAG holds nodes and validates their dependency relationships.
type DAG struct {
	nodes map[string]*Node
	order []string // topologically sorted node names
}

// NewDAG registers nodes and computes a stable topological order. It rejects
// unknown dependencies and cycles before any work runs.
func NewDAG(nodes []Node) (*DAG, error) {
	d := &DAG{nodes: map[string]*Node{}}
	for i := range nodes {
		n := nodes[i]
		if n.Name == "" {
			return nil, fmt.Errorf("pipeline: node %d has empty name", i)
		}
		if _, dup := d.nodes[n.Name]; dup {
			return nil, fmt.Errorf("pipeline: duplicate node name %q", n.Name)
		}
		d.nodes[n.Name] = &n
	}
	order, err := topoSort(d.nodes)
	if err != nil {
		return nil, err
	}
	d.order = order
	return d, nil
}

// Order returns the topological execution order.
func (d *DAG) Order() []string { return append([]string{}, d.order...) }

// Node looks up a registered node by name.
func (d *DAG) Node(name string) (*Node, bool) {
	n, ok := d.nodes[name]
	return n, ok
}

// FirstBlocking returns the first node (in topological order) anywhere in the
// transitive dependency closure of name that has failed, so callers report
// the root blocking dependency rather than the downstream symptom.
func (d *DAG) FirstBlocking(name string, failed map[string]bool) (string, bool) {
	if _, ok := d.nodes[name]; !ok {
		return "", false
	}
	closure := map[string]bool{name: true}
	var walk func(string)
	walk = func(cur string) {
		node := d.nodes[cur]
		for _, dep := range node.Deps {
			if !closure[dep] {
				closure[dep] = true
				walk(dep)
			}
		}
	}
	walk(name)
	for _, dep := range d.order {
		if closure[dep] && failed[dep] {
			return dep, true
		}
	}
	return "", false
}

func topoSort(nodes map[string]*Node) ([]string, error) {
	indeg := map[string]int{}
	dependents := map[string][]string{}
	for name, n := range nodes {
		indeg[name] = 0
		for _, dep := range n.Deps {
			if _, ok := nodes[dep]; !ok {
				return nil, fmt.Errorf("pipeline: node %q depends on unknown node %q", name, dep)
			}
			dependents[dep] = append(dependents[dep], name)
			indeg[name]++
		}
	}
	// Deterministic Kahn process: seed with zero-indegree names sorted.
	var ready []string
	for name, d := range indeg {
		if d == 0 {
			ready = append(ready, name)
		}
	}
	sort.Strings(ready)
	var order []string
	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		order = append(order, name)
		next := append([]string{}, dependents[name]...)
		sort.Strings(next)
		for _, child := range next {
			indeg[child]--
			if indeg[child] == 0 {
				ready = append(ready, child)
			}
		}
	}
	if len(order) != len(nodes) {
		return nil, fmt.Errorf("pipeline: dependency cycle detected")
	}
	return order, nil
}
