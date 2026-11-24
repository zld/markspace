package dag

import (
	"fmt"

	mpierr "mediapipeline/internal/errors"
)

// State is the persistent status of one node, used for idempotent resumes.
type State struct {
	// Status is "done" once the node has committed.
	Status string
	// InputDigest is the digest of the node's input at completion.
	InputDigest string
	// ConfigDigest is the digest of the node's configuration.
	ConfigDigest string
}

// StateStore is the minimal persistence surface a Scheduler needs. The
// checkpoint package provides a file-backed implementation.
type StateStore interface {
	// Get returns the stored state, or ok=false when the node never ran.
	Get(node string) (State, bool)
	// Put persists state for a node keyed by its idempotency tuple.
	Put(node string, st State) error
}

// NodeFunc executes one node. Returning an error marks the run failed; the
// node can be retried on a later Scheduler run.
type NodeFunc func(name string) error

// NodeResult reports what the scheduler did with one node.
type NodeResult struct {
	Node    string
	Reused  bool // true when a committed checkpoint made the run unnecessary
	ReRan   bool // true when the node body executed in this run
	Started bool // false when an unfinished dependency blocked it
}

// Blocked identifies the first dependency that prevented a node from starting.
type Blocked struct {
	Node       string
	Dependency string
	Reason     string
}

// RunReport summarizes one scheduler execution.
type RunReport struct {
	Results []NodeResult
	// Blocked is set when a node could not start because a dependency failed
	// or never ran. Only the first occurrence is recorded.
	Blocked *Blocked
	// FailedNode is the node whose NodeFunc returned an error, if any.
	FailedNode string
	FailedErr  *mpierr.MediaError
}

// ReusedCount returns how many nodes were skipped via checkpoint reuse.
func (r RunReport) ReusedCount() int {
	n := 0
	for _, x := range r.Results {
		if x.Reused {
			n++
		}
	}
	return n
}

// Scheduler runs a validated graph against a StateStore.
type Scheduler struct {
	graph *Graph
	store StateStore
	funcs map[string]NodeFunc
}

// NewScheduler wires a graph, store and node implementations.
func NewScheduler(g *Graph, store StateStore, funcs map[string]NodeFunc) *Scheduler {
	return &Scheduler{graph: g, store: store, funcs: funcs}
}

// Run executes nodes in topological order. Nodes already marked done with a
// matching idempotency tuple are reused; on failure execution stops and the
// first blocked dependent is reported.
func (s *Scheduler) Run() RunReport {
	rep := RunReport{}
	failed := map[string]bool{}
	completed := map[string]bool{} // nodes that finished in this run
	for _, name := range s.graph.order {
		deps, _ := s.graph.DepsOf(name)

		// Idempotency: a completed node with recorded digests is never rerun.
		if st, ok := s.store.Get(name); ok && st.Status == "done" {
			rep.Results = append(rep.Results, NodeResult{Node: name, Reused: true})
			completed[name] = true
			continue
		}
		// Dependency gate: find the first dep that did not complete.
		if block := s.firstBlocked(deps, failed, completed); block != nil {
			block.Node = name
			if rep.Blocked == nil {
				rep.Blocked = block
			}
			rep.Results = append(rep.Results, NodeResult{Node: name, Started: false})
			failed[name] = true
			continue
		}
		fn, ok := s.funcs[name]
		if !ok {
			err := mpierr.New(mpierr.CatDAG, 0, name, "no implementation registered")
			rep.FailedNode, rep.FailedErr = name, err
			rep.Results = append(rep.Results, NodeResult{Node: name, ReRan: true})
			failed[name] = true
			continue
		}
		if err := fn(name); err != nil {
			failed[name] = true
			rep.FailedNode = name
			me, ok := err.(*mpierr.MediaError)
			if !ok {
				me = mpierr.New(mpierr.CatUnknown, 0, name, err.Error())
			}
			rep.FailedErr = me
			rep.Results = append(rep.Results, NodeResult{Node: name, ReRan: true})
			continue
		}
		rep.Results = append(rep.Results, NodeResult{Node: name, ReRan: true})
		completed[name] = true
	}
	return rep
}

// firstBlocked returns the first dependency that is neither done nor about to
// complete in this run, preferring dependencies that failed outright.
func (s *Scheduler) firstBlocked(deps []string, failed, completed map[string]bool) *Blocked {
	for _, d := range deps {
		if failed[d] {
			return &Blocked{Dependency: d, Reason: "dependency failed"}
		}
		if completed[d] {
			continue // finished successfully earlier in this run
		}
		st, ok := s.store.Get(d)
		if !ok || st.Status != "done" {
			return &Blocked{Dependency: d, Reason: "dependency not complete"}
		}
	}
	return nil
}

// MarkDone persists a node's completion with its idempotency digests.
func (s *Scheduler) MarkDone(name, inputDigest, configDigest string) error {
	if _, ok := s.graph.nodes[name]; !ok {
		return fmt.Errorf("dag: cannot mark unknown node %q", name)
	}
	return s.store.Put(name, State{Status: "done", InputDigest: inputDigest, ConfigDigest: configDigest})
}
