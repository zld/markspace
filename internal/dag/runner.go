package dag

import (
	"errors"
	"fmt"
	"sync"

	"archivist/internal/model"
)

// Config controls runner retry behavior.
type Config struct {
	MaxRetries int // retries for transient failures (0 = fail on first)
}

// CommittedArtifact records one successful, idempotent node output.
type CommittedArtifact struct {
	NodeID         string
	IdempotencyKey string
	Stage          string
}

// NodeResult captures the terminal state and attempt count of one node.
type NodeResult struct {
	NodeID   string
	State    NodeState
	Attempts int
	Err      error
}

// Run is one graph execution with its commit ledger.
type Run struct {
	mu        sync.Mutex
	cfg       Config
	graph     *Graph
	committed map[string]CommittedArtifact // idempotency key -> artifact
	results   map[string]*NodeResult
	outputs   map[string]any
}

// NewRunner constructs a runner for an already-validated graph.
func NewRunner(graph *Graph, cfg Config) *Run {
	return &Run{
		cfg:       cfg,
		graph:     graph,
		committed: map[string]CommittedArtifact{},
		results:   map[string]*NodeResult{},
		outputs:   map[string]any{},
	}
}

// Execute runs every node in topological order. A permanent failure or
// exhausted retries mark the node failed and every dependent blocked.
func (r *Run) Execute() (*Report, error) {
	for _, id := range r.graph.Order() {
		node := r.graph.Node(id)
		// Skip nodes whose upstream never succeeded.
		if r.anyDependencyBlocked(node) {
			r.results[id] = &NodeResult{NodeID: id, State: StateBlocked}
			continue
		}
		key := node.IdempotencyKey()
		// Idempotency: a node already committed under this key never reruns.
		r.mu.Lock()
		_, done := r.committed[key]
		r.mu.Unlock()
		if done {
			r.results[id] = &NodeResult{NodeID: id, State: StateSucceeded, Attempts: 0}
			continue
		}
		r.runNode(node, key)
	}
	return r.buildReport(), nil
}

func (r *Run) runNode(node *Node, key string) {
	ctx := &RunContext{NodeID: node.ID, Results: map[string]any{}}
	for dep, out := range r.outputs {
		ctx.Results[dep] = out
	}
	var lastErr error
	attempts := 0
	maxAttempts := 1
	if !node.Permanent {
		maxAttempts = 1 + r.cfg.MaxRetries
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		attempts++
		lastErr = node.Execute(ctx)
		if lastErr == nil {
			r.mu.Lock()
			r.committed[key] = CommittedArtifact{
				NodeID: node.ID, IdempotencyKey: key, Stage: node.Stage,
			}
			r.mu.Unlock()
			r.results[node.ID] = &NodeResult{
				NodeID: node.ID, State: StateSucceeded, Attempts: attempts,
			}
			if po, ok := nodeResultOutput(node); ok {
				r.outputs[node.ID] = po
			}
			return
		}
		// Permanent errors are not retried regardless of configuration.
		var ae *model.ArchiveError
		if node.Permanent || (errors.As(lastErr, &ae) && errors.Is(ae, model.ErrPermanentFailure)) {
			break
		}
	}
	r.results[node.ID] = &NodeResult{
		NodeID: node.ID, State: StateFailed, Attempts: attempts, Err: lastErr,
	}
}

// nodeResultOutput lets a node publish an output value via the optional
// OutputNode interface without coupling the core Node struct to payloads.
func nodeResultOutput(node *Node) (any, bool) {
	if p, ok := any(node).(interface{ Output() (any, bool) }); ok {
		return p.Output()
	}
	return nil, false
}

func (r *Run) anyDependencyBlocked(node *Node) bool {
	for _, dep := range node.Deps {
		res := r.results[dep]
		if res == nil || res.State == StateFailed || res.State == StateBlocked {
			return true
		}
	}
	return false
}

// Committed reports whether an idempotency key already has an artifact.
func (r *Run) Committed(key string) (CommittedArtifact, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.committed[key]
	return a, ok
}

// CommitCount returns the number of distinct artifacts committed this run.
func (r *Run) CommitCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.committed)
}

// Report is the deterministic summary of a graph execution.
type Report struct {
	Order       []string
	Results     map[string]*NodeResult
	FirstBlocker *NodeResult
	Blocked     []string
	Committed   int
}

func (r *Run) buildReport() *Report {
	rep := &Report{
		Order:     r.graph.Order(),
		Results:   r.results,
		Committed: r.CommitCount(),
	}
	// First blocker = earliest failed node in topological order.
	for _, id := range r.graph.Order() {
		res := r.results[id]
		if res != nil && res.State == StateFailed {
			rep.FirstBlocker = res
			break
		}
	}
	for _, id := range r.graph.Order() {
		if r.results[id] != nil && r.results[id].State == StateBlocked {
			rep.Blocked = append(rep.Blocked, id)
		}
	}
	return rep
}

// Failed returns an error describing the first blocking failure, if any.
func (rep *Report) Failed() error {
	if rep.FirstBlocker == nil {
		return nil
	}
	return fmt.Errorf("node %q failed after %d attempt(s): %w",
		rep.FirstBlocker.NodeID, rep.FirstBlocker.Attempts, rep.FirstBlocker.Err)
}
