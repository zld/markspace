package dag

import (
	"errors"
	"sync"

	"pwf/internal/media"
)

// RunContext gives a node access to its successful dependencies' outputs.
type RunContext struct {
	NodeKey string
	outputs map[string][]byte
}

// Output returns the committed output bytes of a dependency node.
func (c *RunContext) Output(depKey string) ([]byte, bool) {
	v, ok := c.outputs[depKey]
	return v, ok
}

// Committer persists a node's output atomically and serves previously
// committed bytes back to dependent nodes.
type Committer interface {
	Commit(nodeKey string, data []byte) (sha256Hex string, err error)
	Read(nodeKey string) ([]byte, error)
}

// RunResult records how one node ended.
type RunResult struct {
	Key    string
	Status Status
	SHA256 string
	Err    error
	Reused bool // served from checkpoint without recomputation
}

// Executor runs a graph with bounded concurrency. Nodes are dispatched
// only once every dependency has succeeded. On failure or interruption,
// downstream nodes are marked blocked and the first failed node in stable
// topological order is reported as the blocker.
type Executor struct {
	Graph   *Graph
	Workers int
	Commit  Committer
	// Skip, when it returns (sha,true), lets a checkpointed node be reused.
	Skip func(nodeKey string) (string, bool)
	// StopAfterKey simulates an interruption: once this node commits, the
	// run halts and every not-yet-started node stays pending/blocked.
	StopAfterKey string
}

type job struct {
	key string
	pos int
}

// Execute runs all nodes, returning per-node results in topological order.
func (e *Executor) Execute() ([]RunResult, error) {
	if e.Workers <= 0 {
		e.Workers = 1
	}
	order := e.Graph.Order()
	idx := map[string]int{}
	results := make([]RunResult, len(order))
	indeg := map[string]int{}
	children := map[string][]string{}
	for i, k := range order {
		idx[k] = i
		results[i] = RunResult{Key: k, Status: StatusPending}
		indeg[k] = len(e.Graph.nodes[k].Deps)
		for _, d := range e.Graph.nodes[k].Deps {
			children[d] = append(children[d], k)
		}
	}

	var (
		mu       sync.Mutex
		ready    []string
		running  int
		finished int
		stopped  bool
		terminal error
		awake    = make(chan struct{}, 1)
	)
	for _, k := range order {
		if indeg[k] == 0 {
			ready = append(ready, k)
		}
	}
	signal := func() {
		select {
		case awake <- struct{}{}:
		default:
		}
	}

	worker := func(j job) {
		node := e.Graph.nodes[j.key]
		rc := &RunContext{NodeKey: j.key, outputs: map[string][]byte{}}
		for _, d := range node.Deps {
			if data, err := e.Commit.Read(d); err == nil {
				rc.outputs[d] = data
			}
		}
		data, runErr := node.Run(rc)
		if runErr == nil {
			sha, cErr := e.Commit.Commit(j.key, data)
			if cErr != nil {
				runErr = media.NewError(media.KindCheckpoint, media.StageSchedule,
					"atomic commit failed", cErr)
			} else {
				mu.Lock()
				results[j.pos] = RunResult{Key: j.key, Status: StatusSucceeded, SHA256: sha}
				finished++
				if j.key == e.StopAfterKey {
					stopped = true
					terminal = errors.New("simulated interruption after " + j.key)
				} else {
					e.enqueueChildren(j.key, indeg, children, &ready, results, idx)
				}
				running--
				signal()
				mu.Unlock()
				return
			}
		}
		mu.Lock()
		if results[j.pos].Status != StatusSucceeded {
			results[j.pos] = RunResult{Key: j.key, Status: StatusFailed, Err: runErr}
		}
		finished++
		if terminal == nil {
			terminal = runErr
			stopped = true
		}
		running--
		signal()
		mu.Unlock()
	}

	mu.Lock()
	for finished < len(order) {
		// A worker failure or simulated interruption ends scheduling.
		if stopped {
			break
		}
		// Reuse every checkpointed node present in the ready queue, leaving
		// the rest queued for execution. A single non-reusable node must not
		// block reuse of nodes sitting behind it.
		if e.Skip != nil {
			remaining := ready[:0]
			for _, k := range ready {
				if sha, ok := e.Skip(k); ok {
					results[idx[k]] = RunResult{Key: k, Status: StatusSucceeded, SHA256: sha, Reused: true}
					finished++
					e.enqueueChildren(k, indeg, children, &remaining, results, idx)
					continue
				}
				remaining = append(remaining, k)
			}
			ready = remaining
		}
		if stopped {
			break
		}
		for len(ready) > 0 && running < e.Workers {
			k := ready[0]
			ready = ready[1:]
			results[idx[k]].Status = StatusRunning
			running++
			go worker(job{key: k, pos: idx[k]})
		}
		if finished+running == len(order) && running == 0 {
			break
		}
		if running == 0 && len(ready) == 0 {
			break // nothing schedulable (should only happen after a failure)
		}
		mu.Unlock()
		<-awake
		mu.Lock()
	}
	markBlocked(results, e.Graph, idx)
	out := append([]RunResult(nil), results...)
	mu.Unlock()

	for _, r := range out {
		if r.Status == StatusFailed {
			return out, &BlockedError{FirstBlocker: r.Key, Cause: r.Err, Results: out}
		}
	}
	if terminal != nil {
		return out, &BlockedError{FirstBlocker: "", Cause: terminal, Results: out, Interrupted: true}
	}
	return out, nil
}

// enqueueChildren decrements indegrees of a finished node's children and
// enqueues newly-ready nodes. Caller must hold mu.
func (e *Executor) enqueueChildren(parent string, indeg map[string]int, children map[string][]string,
	ready *[]string, results []RunResult, idx map[string]int) {
	kids := append([]string(nil), children[parent]...)
	for _, k := range kids {
		if results[idx[k]].Status != StatusPending {
			continue
		}
		indeg[k]--
		if indeg[k] == 0 {
			*ready = insertSorted(*ready, k)
		}
	}
}

// markBlocked flags every still-pending node whose deps did not succeed.
func markBlocked(results []RunResult, g *Graph, idx map[string]int) {
	changed := true
	for changed {
		changed = false
		for i := range results {
			if results[i].Status != StatusPending {
				continue
			}
			for _, d := range g.nodes[results[i].Key].Deps {
				if results[idx[d]].Status != StatusSucceeded {
					results[i].Status = StatusBlocked
					changed = true
					break
				}
			}
		}
	}
}

// BlockedError carries the first node that blocked the graph.
type BlockedError struct {
	FirstBlocker string
	Cause        error
	Results      []RunResult
	Interrupted  bool
}

func (b *BlockedError) Error() string {
	if b.Interrupted {
		return "pipeline interrupted before completion"
	}
	return "first blocker: " + b.FirstBlocker
}

func (b *BlockedError) Unwrap() error { return b.Cause }
