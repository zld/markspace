package dag

import (
	"encoding/json"
	"fmt"

	"mediasync/internal/clock"
)

// CheckpointStore persists completed node outputs keyed by idempotent key.
// The journal in package store implements this interface atomically.
type CheckpointStore interface {
	// Load returns (output,true) only when key matches an intact checkpoint.
	Load(key string) (any, bool)
	// Store writes an output; implementations must make writes idempotent
	// and atomic (temp file + rename).
	Store(key string, output any) error
}

// AnnotatingStore is implemented by stores that also record how a
// checkpoint was derived, so reopening a journal can verify that every
// dependency checkpoint is itself present and completed.
type AnnotatingStore interface {
	RecordNode(key, node string, depKeys []string) error
}

// Failure is the terminal outcome for a node that exhausted its retries.
type Failure struct {
	Node     string
	Attempts int
	Err      error
}

// BlockedDependency names the first upstream node that prevents progress
// and the full dependency chain from the blocked node down to it.
type BlockedDependency struct {
	Node   string
	BlockedBy string
	Chain  []string
	Reason string
}

func (b *BlockedDependency) Error() string {
	return fmt.Sprintf("node %q blocked by %q (chain %v): %s",
		b.Node, b.BlockedBy, b.Chain, b.Reason)
}

// Scheduler runs a graph with bounded per-node retries and checkpoint reuse.
type Scheduler struct {
	graph      *Graph
	retryLimit int
	store      CheckpointStore
	failures   map[string]*Failure
}

// NewScheduler builds a scheduler. retryLimit is the number of *retries*
// after the first attempt, so retryLimit=2 means up to 3 attempts.
func NewScheduler(g *Graph, retryLimit int, store CheckpointStore) (*Scheduler, error) {
	if retryLimit < 0 {
		return nil, clock.NewError(clock.ErrInvalidArgument, "retryLimit must be >= 0")
	}
	return &Scheduler{graph: g, retryLimit: retryLimit, store: store,
		failures: make(map[string]*Failure)}, nil
}

// Result reports the scheduling outcome.
type Result struct {
	Completed map[string]*Node
	Failures  map[string]*Failure
	Blocked   *BlockedDependency
	Reused    int // outputs served from the checkpoint store
}

// Run executes every node in topological order. A node whose dependencies
// did not complete is never executed; the first such node in stable order is
// reported together with its dependency chain.
func (s *Scheduler) Run() *Result {
	res := &Result{Completed: make(map[string]*Node), Failures: s.failures}
	depKeys := make(map[string]string)

	for _, n := range s.graph.Order() {
		if block := s.firstBlocked(n); block != nil {
			if res.Blocked == nil {
				res.Blocked = block
			}
			n.Failed, n.Err = true, block
			continue
		}
		key := IdempotentKey(n, depKeysFor(n, depKeys))
		n.Key = key

		if s.store != nil {
			if raw, ok := s.store.Load(key); ok {
				out, err := s.hydrate(n, raw)
				if err != nil {
					n.Failed, n.Err = true, err
					s.failures[n.Name] = &Failure{Node: n.Name, Err: err}
					continue
				}
				n.Completed, n.Output, res.Reused = true, out, res.Reused+1
				depKeys[n.Name] = key
				res.Completed[n.Name] = n
				continue
			}
		}

		out, err := s.runWithRetries(n)
		if err != nil {
			// Keep any partial output for diagnostics, but the node is not
			// completed and nothing is checkpointed, so downstream nodes are
			// blocked rather than consuming an untrusted result.
			n.Failed, n.Err = true, err
			if out != nil {
				n.Output = out
			}
			s.failures[n.Name] = &Failure{Node: n.Name, Attempts: n.Attempts, Err: err}
			continue
		}
		if s.store != nil {
			if perr := s.store.Store(key, out); perr != nil {
				n.Failed, n.Err = true, perr
				s.failures[n.Name] = &Failure{Node: n.Name, Attempts: n.Attempts, Err: perr}
				continue
			}
			if as, ok := s.store.(AnnotatingStore); ok {
				dks := make([]string, 0, len(n.Deps))
				for _, d := range n.Deps {
					dks = append(dks, depKeys[d])
				}
				if perr := as.RecordNode(key, n.Name, dks); perr != nil {
					n.Failed, n.Err = true, perr
					s.failures[n.Name] = &Failure{Node: n.Name, Attempts: n.Attempts, Err: perr}
					continue
				}
			}
		}
		n.Completed, n.Output = true, out
		depKeys[n.Name] = key
		res.Completed[n.Name] = n
	}
	return res
}

// hydrate turns a checkpoint payload back into the node's typed output. When
// the node declares an OutputFactory the JSON is unmarshalled into that
// concrete type; otherwise the raw payload is returned unchanged.
func (s *Scheduler) hydrate(n *Node, raw any) (any, error) {
	if n.OutputFactory == nil {
		return raw, nil
	}
	rm, ok := raw.(json.RawMessage)
	if !ok {
		return raw, nil // in-memory stores may hand back values directly
	}
	out := n.OutputFactory()
	if err := json.Unmarshal(rm, out); err != nil {
		return nil, clock.NewError(clock.ErrCorrupt,
			"checkpoint for node %q is not decodable: %v", n.Name, err)
	}
	return out, nil
}

// runWithRetries executes a node, retrying transient errors up to retryLimit.
// Retries never produce duplicate outputs: Store happens once, only after a
// successful Run, and checkpoint writes are idempotent on key. When every
// attempt fails the last non-nil output (if any) is still returned alongside
// the terminal error for diagnostic use.
func (s *Scheduler) runWithRetries(n *Node) (any, error) {
	deps := make(map[string]*Node, len(n.Deps))
	for _, d := range n.Deps {
		deps[d] = s.graph.nodes[d]
	}
	var err error
	var lastOut any
	for attempt := 0; attempt <= s.retryLimit; attempt++ {
		n.Attempts = attempt + 1
		var out any
		out, err = n.Run(n, deps)
		if out != nil {
			lastOut = out
		}
		if err == nil {
			return out, nil
		}
		// Structured non-transparent errors (invalid argument etc.) are not
		// worth retrying: surface immediately.
		if me, ok := err.(*clock.MediaError); ok && me.Code == clock.ErrInvalidArgument {
			return lastOut, err
		}
	}
	return lastOut, clock.NewError(clock.ErrLimitExceeded,
		"node %q exhausted %d attempts: %v", n.Name, n.Attempts, err)
}

// firstBlocked walks the node's dependencies and returns the nearest failed
// upstream node with the chain linking them.
func (s *Scheduler) firstBlocked(n *Node) *BlockedDependency {
	for _, d := range n.Deps {
		dn := s.graph.nodes[d]
		if dn.Completed {
			continue
		}
		chain := []string{n.Name, d}
		cur := d
		if f, ok := s.failures[d]; ok {
			return &BlockedDependency{Node: n.Name, BlockedBy: d, Chain: chain,
				Reason: f.Err.Error()}
		}
		// Transitive: descend until we hit the node that actually failed.
		for {
			next := s.firstFailedAncestor(s.graph.nodes[cur])
			if next == "" {
				break
			}
			if next == cur {
				break
			}
			chain = append(chain, next)
			cur = next
		}
		if f, ok := s.failures[cur]; ok {
			return &BlockedDependency{Node: n.Name, BlockedBy: cur, Chain: chain,
				Reason: f.Err.Error()}
		}
		return &BlockedDependency{Node: n.Name, BlockedBy: cur, Chain: chain,
			Reason: "dependency did not complete"}
	}
	return nil
}

func (s *Scheduler) firstFailedAncestor(n *Node) string {
	for _, d := range n.Deps {
		dn := s.graph.nodes[d]
		if !dn.Completed {
			if s.failures[d] != nil {
				return d
			}
			if deep := s.firstFailedAncestor(dn); deep != "" {
				return deep
			}
			return d
		}
	}
	return ""
}

func depKeysFor(n *Node, all map[string]string) map[string]string {
	out := make(map[string]string, len(n.Deps))
	for _, d := range n.Deps {
		if k, ok := all[d]; ok {
			out[d] = k
		}
	}
	return out
}
