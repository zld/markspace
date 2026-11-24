package pipeline

import (
	"fmt"
)

// Exit codes promised by the CLI contract.
const (
	ExitOK           = 0
	ExitCorruptInput = 2
	ExitBlocked      = 3
)

// BlockedError names the first failed/blocking dependency in topological
// order; the CLI maps it to exit code 3.
type BlockedError struct {
	Node    string
	Blocker string
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("pipeline: node %q is blocked by failed dependency %q", e.Node, e.Blocker)
}

// Runner executes a DAG against a checkpoint store and records what it did.
type Runner struct {
	dag   *DAG
	store *Store
	// Reused counts nodes whose checkpoint was reused without rerunning.
	Reused int
	// Ran counts nodes actually executed in this invocation.
	Ran int
	// Failed maps every node that could not complete to its error.
	Failed map[string]error
	// interruptAfter stops the run once the named node completes (if set).
	interruptAfter string
}

// NewRunner wires a validated DAG to its persistent store.
func NewRunner(dag *DAG, store *Store) *Runner {
	return &Runner{dag: dag, store: store, Failed: map[string]error{}}
}

// WithInterruptAfter arranges for Execute to stop after the named node
// finishes, simulating a crash so recovery can be tested deterministically.
func (r *Runner) WithInterruptAfter(node string) *Runner {
	r.interruptAfter = node
	return r
}

// Result pairs a node's output summary with its checkpoint record.
type Result struct {
	Output     string
	Checkpoint Checkpoint
}

// Execute walks the topological order once. A node is skipped when a valid
// checkpoint exists; otherwise it runs, and its result is persisted. A failed
// dependency blocks dependants, which surface the root blocker.
func (r *Runner) Execute() (map[string]Result, error) {
	results := map[string]Result{}
	outputs := map[string]string{}
	interrupted := false

	for _, name := range r.dag.Order() {
		if interrupted {
			// Remaining nodes are simply not run; their checkpoints stay
			// absent so a resume recomputes only them.
			break
		}
		node, _ := r.dag.Node(name)

		// Dependency gate: report the first blocker in topological order.
		if blocker, ok := r.dag.FirstBlocking(name, r.failedSet()); ok {
			err := &BlockedError{Node: name, Blocker: blocker}
			r.Failed[name] = err
			return results, err
		}

		key, err := node.Key()
		if err != nil {
			r.Failed[name] = err
			return results, err
		}
		if cp, ok, _ := r.store.Load(key); ok && r.store.Valid(cp, node) {
			// All dependencies must also have live checkpoints to reuse.
			if r.depsCheckpointed(node) {
				r.Reused++
				results[name] = Result{Output: cp.OutputHash, Checkpoint: cp}
				outputs[name] = cp.OutputHash
				interrupted = r.maybeInterrupt(name)
				continue
			}
		}

		// Recompute: only missing or invalid nodes reach this point.
		ctx := &RunContext{Node: node, outputs: outputs}
		out, runErr := node.Run(ctx)
		if runErr != nil {
			r.Failed[name] = runErr
			return results, runErr
		}
		cp := Checkpoint{
			Key: key, Dependencies: append([]string{}, node.Deps...),
			OutputHash: out, NodeVersion: CodeVersion,
			SourceOffsets: r.sourceOffsets(ctx),
		}
		if err := r.store.Save(cp); err != nil {
			r.Failed[name] = err
			return results, err
		}
		r.Ran++
		results[name] = Result{Output: out, Checkpoint: cp}
		outputs[name] = out
		interrupted = r.maybeInterrupt(name)
	}
	return results, nil
}

func (r *Runner) maybeInterrupt(name string) bool {
	return r.interruptAfter != "" && name == r.interruptAfter
}

func (r *Runner) failedSet() map[string]bool {
	set := make(map[string]bool, len(r.Failed))
	for name := range r.Failed {
		set[name] = true
	}
	return set
}

func (r *Runner) depsCheckpointed(node *Node) bool {
	for _, dep := range node.Deps {
		dn, ok := r.dag.Node(dep)
		if !ok {
			return false
		}
		key, err := dn.Key()
		if err != nil {
			return false
		}
		if _, ok, _ := r.store.Load(key); !ok {
			return false
		}
	}
	return true
}

func (r *Runner) sourceOffsets(ctx *RunContext) []SourceOffset {
	// Nodes expose provenance through the context when they choose to; the
	// default record carries no offsets rather than inventing them.
	return ctx.offsets
}
