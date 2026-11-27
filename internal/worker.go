package rtpreplay

import (
	"fmt"
	"sort"
)

// Artifact is the reproducible output of one node. Value holds the in-memory
// payload and is not serialized; Digest is its content hash.
type Artifact struct {
	Node           NodeID `json:"node"`
	InputDigest    string `json:"input_digest"`
	ConfigDigest   string `json:"config_digest"`
	IdempotencyKey string `json:"idempotency_key"`
	Digest         string `json:"artifact_sha256"`
	Attempts       int    `json:"attempts"`
	Cached         bool   `json:"cached"`
	Value          any    `json:"-"`
}

// NodeResult is the terminal state of one node in a run.
type NodeResult struct {
	Node      NodeID    `json:"node"`
	State     string    `json:"state"` // succeeded, failed, blocked
	Attempts  int       `json:"attempts"`
	Error     *Error    `json:"error,omitempty"`
	BlockedBy NodeID    `json:"blocked_by,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Output    *Artifact `json:"output,omitempty"`
}

// Node states.
const (
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateBlocked   = "blocked"
)

// RunResult aggregates a DAG execution. Completed is true only when every node
// succeeded; a blocked or failed node forces it false.
type RunResult struct {
	Results        []*NodeResult `json:"results"`
	Completed      bool          `json:"completed"`
	FirstBlocker   *NodeResult   `json:"first_blocker,omitempty"`
	ArtifactDigest string        `json:"artifact_sha256"`
	Artifacts      []*Artifact   `json:"artifacts"`
}

// Worker executes a DAG with idempotency: each node's key is derived from the
// digest of its inputs plus a configuration digest. A node whose key matches a
// previously completed artifact is served from cache and yields the same
// artifact digest, so reruns are byte-for-byte reproducible.
type Worker struct {
	configDigest string
	cache        map[string]*Artifact
	retryHook    func(n NodeID, attempt int, err error)
}

// NewWorker creates a worker bound to a configuration digest.
func NewWorker(configDigest string) *Worker {
	return &Worker{configDigest: configDigest, cache: make(map[string]*Artifact)}
}

// SeedCache preloads artifacts from a previous run, enabling cross-run
// idempotency checks. Seeded artifacts keep their original Attempts value.
func (w *Worker) SeedCache(arts []*Artifact) {
	for _, a := range arts {
		if a == nil {
			continue
		}
		cp := *a
		cp.Cached = false
		w.cache[cp.IdempotencyKey] = &cp
	}
}

// SetRetryHook installs a callback invoked before each retry. Tests and fault
// fixtures use it to observe recovery.
func (w *Worker) SetRetryHook(hook func(n NodeID, attempt int, err error)) {
	w.retryHook = hook
}

// IdempotencyKey returns the stable key for a node given its input digest.
func (w *Worker) IdempotencyKey(node NodeID, inputDigest string) string {
	return sha256Hex([]byte(string(node) + "\x00" + inputDigest + "\x00" + w.configDigest))
}

// Run executes every node in topological order. A node is blocked when any
// dependency failed or was itself blocked; the earliest such node in execution
// order becomes FirstBlocker and Completed stays false.
func (w *Worker) Run(d *Dag) *RunResult {
	res := &RunResult{Results: make([]*NodeResult, 0, len(d.order))}
	outputs := make(map[NodeID]any, len(d.order))
	states := make(map[NodeID]*NodeResult, len(d.order))
	artifacts := make(map[NodeID]*Artifact, len(d.order))
	for _, id := range d.order {
		n := d.nodes[id]
		nr := &NodeResult{Node: id}
		res.Results = append(res.Results, nr)
		states[id] = nr
		if blocker, why := earliestFailedDep(n, states); blocker != "" {
			nr.State = StateBlocked
			nr.BlockedBy = blocker
			nr.Reason = why
			if res.FirstBlocker == nil {
				res.FirstBlocker = nr
			}
			continue
		}
		inputDigest := nodeInputDigest(n, artifacts)
		key := w.IdempotencyKey(id, inputDigest)
		if art, ok := w.cache[key]; ok {
			served := *art
			served.Cached = true
			nr.State = StateSucceeded
			nr.Attempts = art.Attempts
			nr.Output = &served
			outputs[id] = art.Value
			artifacts[id] = &served
			continue
		}
		deps := make(map[NodeID]any, len(n.Deps))
		for _, dep := range n.Deps {
			deps[dep] = outputs[dep]
		}
		val, attempts, err := w.runWithRetry(n, deps)
		nr.Attempts = attempts
		if err != nil {
			nr.State = StateFailed
			nr.Error = AsError(err)
			if res.FirstBlocker == nil {
				res.FirstBlocker = nr
			}
			continue
		}
		art := &Artifact{
			Node: id, InputDigest: inputDigest, ConfigDigest: w.configDigest,
			IdempotencyKey: key, Digest: digestJSON(val), Attempts: attempts, Value: val,
		}
		w.cache[key] = art
		nr.State = StateSucceeded
		nr.Output = art
		outputs[id] = val
		artifacts[id] = art
	}
	res.Completed = true
	for _, r := range res.Results {
		if r.State != StateSucceeded {
			res.Completed = false
			break
		}
	}
	ids := make([]string, 0, len(artifacts))
	for id := range artifacts {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	res.Artifacts = make([]*Artifact, 0, len(ids))
	for _, id := range ids {
		res.Artifacts = append(res.Artifacts, artifacts[NodeID(id)])
	}
	res.ArtifactDigest = runDigest(artifacts)
	return res
}

// runWithRetry retries only structured retryable errors. Non-retryable
// failures and panics stop immediately: a panic is a programming fault, not a
// transient condition, so it is converted to a non-retryable error.
func (w *Worker) runWithRetry(n *Node, deps map[NodeID]any) (val any, attempts int, err error) {
	maxAttempts := n.MaxRetry + 1
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attempts = attempt
		val, err = w.call(n, deps)
		if err == nil {
			return val, attempts, nil
		}
		if !IsRetryable(err) {
			return nil, attempts, err
		}
		if attempt < maxAttempts && w.retryHook != nil {
			w.retryHook(n.ID, attempt, err)
		}
	}
	return nil, attempts, err
}

// call runs a node, converting a panic into a structured error.
func (w *Worker) call(n *Node, deps map[NodeID]any) (val any, err error) {
	defer func() {
		if r := recover(); r != nil {
			val = nil
			err = NewError(CodeInvalidInput, "node", fmt.Sprintf("panic in node %q: %v", n.ID, r)).
				WithRetryable(false)
		}
	}()
	return n.Run(deps)
}

// earliestFailedDep returns the lexicographically first unsuccessful
// dependency and a human-readable cause.
func earliestFailedDep(n *Node, states map[NodeID]*NodeResult) (NodeID, string) {
	var blocked NodeID
	var reason string
	deps := append([]NodeID(nil), n.Deps...)
	sort.Slice(deps, func(i, j int) bool { return deps[i] < deps[j] })
	for _, dep := range deps {
		st := states[dep]
		if st == nil || st.State == StateSucceeded {
			continue
		}
		blocked = dep
		reason = fmt.Sprintf("dependency %q %s", dep, st.State)
		if st.Error != nil {
			reason += ": " + st.Error.Error()
		}
		break
	}
	return blocked, reason
}

// nodeInputDigest hashes the node identity and its dependencies' artifact
// digests in sorted order, so it does not depend on execution order.
func nodeInputDigest(n *Node, artifacts map[NodeID]*Artifact) string {
	deps := append([]NodeID(nil), n.Deps...)
	sort.Slice(deps, func(i, j int) bool { return deps[i] < deps[j] })
	parts := make([]string, 0, len(deps)+1)
	parts = append(parts, string(n.ID))
	for _, dep := range deps {
		if a := artifacts[dep]; a != nil {
			parts = append(parts, string(dep)+"="+a.Digest)
		}
	}
	return sha256Hex([]byte(joinNul(parts)))
}

func joinNul(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\x00"
		}
		out += p
	}
	return out
}

// runDigest hashes the ordered node digests.
func runDigest(artifacts map[NodeID]*Artifact) string {
	ids := make([]string, 0, len(artifacts))
	for id := range artifacts {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, id+"="+artifacts[NodeID(id)].Digest)
	}
	return sha256Hex([]byte(joinNul(parts)))
}
