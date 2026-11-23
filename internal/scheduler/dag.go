// Package scheduler runs a dependency DAG with idempotent, digest-keyed
// retries. It never re-submits an artifact whose idempotency key has already
// succeeded, and on failure it reports the first blocked node in topological
// order.
package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"archivecheck/internal/model"
)

// Node is one DAG task.
type Node struct {
	// ID is unique within the graph.
	ID string
	// Deps are node IDs that must succeed before this node runs.
	Deps []string
	// InputDigest is the content digest of everything this node consumes.
	InputDigest string
	// ParamsDigest is the digest of the node's configuration.
	ParamsDigest string
	// Run performs the work. It must be safe to call more than once with the
	// same key: a retry after an ambiguous failure must not duplicate output.
	Run func(ctx context.Context, key Key) (Artifact, error)
	// MaxAttempts bounds transient retries (0 or 1 means a single attempt).
	MaxAttempts int
}

// Key is the node's idempotency key.
type Key struct {
	NodeID       string
	InputDigest  string
	ParamsDigest string
}

// Digest returns the stable hex digest of the key.
func (k Key) Digest() string {
	h := sha256.New()
	h.Write([]byte(k.NodeID))
	h.Write([]byte{0})
	h.Write([]byte(k.InputDigest))
	h.Write([]byte{0})
	h.Write([]byte(k.ParamsDigest))
	return hex.EncodeToString(h.Sum(nil))
}

// Artifact is the recorded result of a successful node.
type Artifact struct {
	// Digest is the content digest of the produced output.
	Digest string
	// Size is the output size in bytes.
	Size int64
	// Detail is free-form audit text folded into the run report.
	Detail string
}

// Status is a node's terminal state.
type Status string

const (
	StatusPending Status = "pending"
	StatusOK      Status = "ok"
	StatusFailed  Status = "failed"
	StatusBlocked Status = "blocked"
)

// NodeResult is the outcome for one node.
type NodeResult struct {
	ID       string   `json:"id"`
	Status   Status   `json:"status"`
	Attempts int      `json:"attempts"`
	Key      string   `json:"key"`
	Artifact Artifact `json:"artifact"`
	// Error is the failing node's message, empty otherwise.
	Error string `json:"error,omitempty"`
	// FaultCode is the code of the error that failed the node.
	FaultCode model.Code `json:"fault_code,omitempty"`
	// Cached is true when the key was already satisfied by a previous run.
	Cached bool `json:"cached"`
}

// Report is the deterministic summary of one graph run.
type Report struct {
	// Order is the topological execution order actually used.
	Order []string `json:"order"`
	// Nodes are per-node results in topological order.
	Nodes []NodeResult `json:"nodes"`
	// Failed is the first failing node ID in topological order, or "".
	Failed string `json:"failed,omitempty"`
	// FirstBlocked is the first node blocked by a failed dependency, in
	// topological order, or "".
	FirstBlocked string `json:"first_blocked,omitempty"`
	// FirstBlockedDependency is the failed dependency that blocks FirstBlocked.
	FirstBlockedDependency string `json:"first_blocked_dependency,omitempty"`
}

// OutputDigest folds every successful artifact digest into one value, so two
// runs that produced the same outputs share an output checksum.
func (r *Report) OutputDigest() string {
	h := sha256.New()
	for _, n := range r.Nodes {
		if n.Status != StatusOK {
			continue
		}
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00", n.ID, n.Artifact.Digest, n.Artifact.Size)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Succeeded reports whether every node finished OK.
func (r *Report) Succeeded() bool {
	for _, n := range r.Nodes {
		if n.Status != StatusOK {
			return false
		}
	}
	return true
}

// ErrCycle is returned when the graph is not acyclic.
var ErrCycle = errors.New("dependency graph has a cycle")

// Graph is a validated DAG.
type Graph struct {
	nodes map[string]*Node
	order []string
}

// NewGraph validates nodes and computes a stable topological order. Ties break
// on node ID so the order is deterministic across runs.
func NewGraph(nodes []Node) (*Graph, error) {
	g := &Graph{nodes: map[string]*Node{}}
	for i := range nodes {
		n := nodes[i]
		if n.ID == "" {
			return nil, errors.New("scheduler: node with empty ID")
		}
		if _, dup := g.nodes[n.ID]; dup {
			return nil, fmt.Errorf("scheduler: duplicate node %q", n.ID)
		}
		cp := n
		g.nodes[n.ID] = &cp
	}
	for _, n := range g.nodes {
		for _, d := range n.Deps {
			if _, ok := g.nodes[d]; !ok {
				return nil, fmt.Errorf("scheduler: node %q depends on unknown node %q", n.ID, d)
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

// Order returns the topological order.
func (g *Graph) Order() []string { return append([]string(nil), g.order...) }

func (g *Graph) topoSort() ([]string, error) {
	indeg := map[string]int{}
	dependents := map[string][]string{}
	for id, n := range g.nodes {
		if _, ok := indeg[id]; !ok {
			indeg[id] = 0
		}
		for _, d := range n.Deps {
			indeg[id]++
			dependents[d] = append(dependents[d], id)
		}
	}
	var ready []string
	for id, deg := range indeg {
		if deg == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	var order []string
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		ds := dependents[id]
		sort.Strings(ds)
		for _, d := range ds {
			indeg[d]--
			if indeg[d] == 0 {
				ready = append(ready, d)
				sort.Strings(ready)
			}
		}
	}
	if len(order) != len(g.nodes) {
		var stuck []string
		for id, deg := range indeg {
			if deg > 0 {
				stuck = append(stuck, id)
			}
		}
		sort.Strings(stuck)
		return nil, fmt.Errorf("%w: %s", ErrCycle, strings.Join(stuck, ","))
	}
	return order, nil
}

// Runner executes a graph.
type Runner struct {
	// Concurrency bounds parallel node execution. Zero runs at the number of
	// ready nodes; use it for deterministic tests.
	Concurrency int
	// Completed maps idempotency-key digests to artifacts from previous runs.
	Completed map[string]Artifact
	// OnComplete is called once per node that actually executed (not cached)
	// so the caller can persist it atomically.
	OnComplete func(key Key, art Artifact) error
}

// Run executes the graph. Independent nodes may run concurrently up to
// Concurrency, but the report is always emitted in topological order and a node
// never starts before every dependency is terminal.
func (r *Runner) Run(ctx context.Context, g *Graph) (*Report, error) {
	limit := r.Concurrency
	if limit <= 0 {
		limit = 1
	}
	results := map[string]*NodeResult{}
	dependents := map[string][]string{}
	pending := map[string]int{}
	for _, id := range g.order {
		pending[id] = len(g.nodes[id].Deps)
		for _, d := range g.nodes[id].Deps {
			dependents[d] = append(dependents[d], id)
		}
	}
	var ready []string
	for _, id := range g.order {
		if pending[id] == 0 {
			ready = append(ready, id)
		}
	}
	type completion struct {
		id  string
		res *NodeResult
	}
	done := make(chan completion, len(g.order))
	running := 0
	terminal := map[string]Status{}
	for len(ready) > 0 || running > 0 {
		for len(ready) > 0 && running < limit {
			id := ready[0]
			ready = ready[1:]
			running++
			go func(id string, blockedBy string) {
				if blockedBy != "" {
					done <- completion{id, &NodeResult{
						ID: id, Status: StatusBlocked, FaultCode: model.CodeDependencyBlocked,
						Error: "blocked by " + blockedBy,
					}}
					return
				}
				done <- completion{id, r.runNode(ctx, g.nodes[id])}
			}(id, blockedDepOf(g.nodes[id], terminal))
		}
		c := <-done
		running--
		results[c.id] = c.res
		terminal[c.id] = c.res.Status
		// Dependents are released in both cases so failed prerequisites surface
		// as blocked nodes instead of silently disappearing.
		for _, d := range dependents[c.id] {
			pending[d]--
			if pending[d] == 0 {
				ready = append(ready, d)
			}
		}
		sortByOrder(ready, g)
	}

	rep := &Report{Order: g.Order()}
	for _, id := range g.order {
		nr, ok := results[id]
		if !ok {
			nr = &NodeResult{ID: id, Status: StatusBlocked, FaultCode: model.CodeDependencyBlocked,
				Error: "unresolved dependency"}
			results[id] = nr
		}
		rep.Nodes = append(rep.Nodes, *nr)
	}
	for _, nr := range rep.Nodes {
		switch nr.Status {
		case StatusFailed:
			if rep.Failed == "" {
				rep.Failed = nr.ID
			}
		case StatusBlocked:
			if rep.FirstBlocked == "" {
				rep.FirstBlocked = nr.ID
				rep.FirstBlockedDependency = blockingDep(g.nodes[nr.ID], rep.Nodes)
			}
		}
	}
	return rep, nil
}

// sortByOrder orders the ready queue by topological position for determinism.
func sortByOrder(ids []string, g *Graph) {
	pos := map[string]int{}
	for i, id := range g.order {
		pos[id] = i
	}
	sort.SliceStable(ids, func(i, j int) bool { return pos[ids[i]] < pos[ids[j]] })
}

// blockedDepOf returns the first dependency of n that is terminal and not OK,
// or "". It must be called from the scheduling loop, not a worker goroutine,
// so it observes a stable terminal map.
func blockedDepOf(n *Node, terminal map[string]Status) string {
	for _, d := range n.Deps {
		if st, ok := terminal[d]; ok && st != StatusOK {
			return d
		}
	}
	return ""
}

// blockingDep finds the dependency of a blocked node that did not succeed.
func blockingDep(n *Node, nodes []NodeResult) string {
	byID := map[string]NodeResult{}
	for _, nr := range nodes {
		byID[nr.ID] = nr
	}
	for _, d := range n.Deps {
		if nr, ok := byID[d]; ok && nr.Status != StatusOK {
			return d
		}
	}
	if len(n.Deps) > 0 {
		return n.Deps[0]
	}
	return ""
}

// runNode executes one node with bounded retries.
func (r *Runner) runNode(ctx context.Context, n *Node) *NodeResult {
	key := Key{NodeID: n.ID, InputDigest: n.InputDigest, ParamsDigest: n.ParamsDigest}
	res := &NodeResult{ID: n.ID, Key: key.Digest()}
	if r.Completed != nil {
		if art, ok := r.Completed[key.Digest()]; ok {
			// Same key already succeeded: reuse the recorded artifact instead
			// of producing it again.
			res.Status = StatusOK
			res.Cached = true
			res.Artifact = art
			return res
		}
	}
	attempts := n.MaxAttempts
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}
		res.Attempts++
		art, err := n.Run(ctx, key)
		if err == nil {
			res.Status = StatusOK
			res.Artifact = art
			if r.OnComplete != nil {
				if perr := r.OnComplete(key, art); perr != nil {
					res.Status = StatusFailed
					res.Error = perr.Error()
					res.FaultCode = codeOf(perr)
					return res
				}
			}
			return res
		}
		lastErr = err
		var f *model.Fault
		if errors.As(err, &f) && f.Severity == model.SeverityError && !retryable(f.Code) {
			break
		}
	}
	res.Status = StatusFailed
	res.Error = lastErr.Error()
	res.FaultCode = codeOf(lastErr)
	return res
}

// retryable reports whether a fault code may be transient. Structural input
// errors are never retried: replaying them cannot succeed and would break the
// "no duplicate submission" guarantee.
func retryable(code model.Code) bool {
	switch code {
	case model.CodeInputMissing, model.CodeInputBadArgs, model.CodeManifestSyntax,
		model.CodeManifestMissingField, model.CodeManifestBadRange, model.CodeManifestTimebase,
		model.CodeManifestBoundary, model.CodeMP4Structure, model.CodeMP4BadLength,
		model.CodeMP4SampleTable, model.CodeMP4NonMonotonic, model.CodeMP4TrackTimebase,
		model.CodeMP4BoxTruncated, model.CodeDependencyBlocked:
		return false
	}
	return true
}

func codeOf(err error) model.Code {
	var f *model.Fault
	if errors.As(err, &f) {
		return f.Code
	}
	if err == nil {
		return ""
	}
	return model.CodeInputBadArgs
}
