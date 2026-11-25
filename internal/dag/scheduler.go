// Package dag executes pipeline stages as a dependency graph with durable
// checkpoints. A node runs only after every dependency succeeded; a node
// whose deps are unmet reports the first blocker instead of running. Task
// keys are stable identifiers, so a resumed run never re-submits work that
// a previous attempt already completed.
package dag

import (
	"context"
	"fmt"
	"sync"

	"mediad/internal/model"
)

// State of a graph node.
type State string

const (
	StatePending   State = "pending"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateSkipped   State = "skipped"
)

// NodeFunc is the unit of work. It receives the checkpoint store so a node
// can persist progress (e.g. input offset) while running.
type NodeFunc func(ctx context.Context, cp *Checkpoint) error

// Node is one graph vertex.
type Node struct {
	Key  string
	Deps []string
	Run  NodeFunc
}

// Result records what happened to one node during an Execute call.
type Result struct {
	Key     string
	State   State
	Blocker string // first unmet dependency, when State == failed/skipped
	Err     error
}

// Scheduler runs nodes in dependency order. It is safe for one Execute at a
// time; internal state is guarded so tests may inspect it concurrently.
type Scheduler struct {
	mu    sync.Mutex
	nodes map[string]*Node
	order []string
	cp    *Checkpoint
}

// NewScheduler builds a scheduler backed by the given checkpoint store.
func NewScheduler(cp *Checkpoint) *Scheduler {
	return &Scheduler{nodes: make(map[string]*Node), cp: cp}
}

// Add registers a node. Duplicate keys replace the previous registration.
func (s *Scheduler) Add(n Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[n.Key]; !ok {
		s.order = append(s.order, n.Key)
	}
	s.nodes[n.Key] = &n
}

// Execute runs every node whose deps succeeded. Already-succeeded nodes
// recorded in the checkpoint are skipped (their work is not repeated). The
// first failure aborts the run; failed nodes and everything downstream of
// them carry the first blocking dependency.
func (s *Scheduler) Execute(ctx context.Context) ([]Result, error) {
	s.mu.Lock()
	keys := append([]string(nil), s.order...)
	nodes := s.nodes
	cp := s.cp
	s.mu.Unlock()

	done := make(map[string]State, len(keys))
	for _, k := range keys {
		if st, ok := cp.NodeState(k); ok {
			done[k] = st
		}
	}
	var results []Result
	var firstErr error
	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		if done[k] == StateSucceeded {
			results = append(results, Result{Key: k, State: StateSucceeded})
			continue
		}
		n := nodes[k]
		if blocker, ok := firstUnmet(n.Deps, done); ok {
			res := Result{Key: k, State: StateFailed, Blocker: blocker,
				Err: fmt.Errorf("%w: %s", model.ErrBlocked, blocker)}
			results = append(results, res)
			done[k] = StateFailed
			if firstErr == nil {
				firstErr = res.Err
			}
			continue
		}
		done[k] = StateRunning
		if err := n.Run(ctx, cp); err != nil {
			done[k] = StateFailed
			results = append(results, Result{Key: k, State: StateFailed, Err: err})
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		done[k] = StateSucceeded
		results = append(results, Result{Key: k, State: StateSucceeded})
	}
	return results, firstErr
}

// firstUnmet returns the first dependency that has not succeeded. A missing
// registration counts as unmet so typos fail loudly instead of hanging.
func firstUnmet(deps []string, done map[string]State) (string, bool) {
	for _, d := range deps {
		if done[d] != StateSucceeded {
			return d, true
		}
	}
	return "", false
}
