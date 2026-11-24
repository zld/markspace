// Package dag runs a small dependency graph deterministically.
//
// Nodes are identified by name; each node's idempotency key is a SHA-256 over
// its kind, its parameters, its declared inputs and the dependency output
// hashes. Independent branches are ready together, and a failed node stops
// only its descendants: unrelated branches always finish.
package dag

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// Status is a node's lifecycle state.
type Status string

const (
	StatusPending Status = "pending"
	StatusReady   Status = "ready"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
	StatusBlocked Status = "blocked"
	// StatusRecovered marks a node whose result was reused from a verified
	// previous run rather than recomputed.
	StatusRecovered Status = "recovered"
)

// Node is one unit of work. Run must be idempotent for the key it receives:
// committing the same key twice may not add or change a committed artifact.
type Node struct {
	Name string
	Kind string
	// Params are the node's explicit parameters, folded into the key.
	Params map[string]string
	// Inputs are byte hashes of the raw inputs this node consumes.
	Inputs []string
	// DependsOn lists node names whose output hashes feed the key.
	DependsOn []string
	// Run produces an output hash. Returning an error fails the node; a
	// typed *Failure lets the runner record a precise location.
	Run func(ctx context.Context, key string, deps map[string]Result) (Result, error)
	// Commit, when non-nil, is called after a successful Run to place the
	// artifact. A second call for the same key must be a no-op.
	Commit func(ctx context.Context, key string, r Result) error
	// Verify re-validates a cached result before reuse.
	Verify func(ctx context.Context, key string, r Result) error
	// AllowRecovery enables reuse of a verified prior result for this key.
	AllowRecovery bool
}

// Result is a node outcome: the output hash plus optional located detail.
type Result struct {
	Hash   string
	Detail string
}

// Cache stores verified results by key.
type Cache interface {
	Get(key string) (Result, bool)
	Put(key string, r Result) error
}

// Failure is an error carrying a precise blocking location.
type Failure struct {
	Node   string
	Stage  string
	Offset int64
	Packet int64
	PID    int
	Kind   string
	Detail string
	Err    error
}

// Error implements error.
func (f *Failure) Error() string {
	msg := fmt.Sprintf("%s blocked at %s", f.Node, f.Stage)
	if f.Offset >= 0 {
		msg += fmt.Sprintf(" offset=%d packet=%d", f.Offset, f.Packet)
	}
	if f.PID >= 0 && f.PID != 0 {
		msg += fmt.Sprintf(" pid=0x%04x", f.PID)
	}
	if f.Detail != "" {
		msg += " " + f.Detail
	}
	if f.Err != nil {
		msg += ": " + f.Err.Error()
	}
	return msg
}

// Unwrap exposes the underlying error.
func (f *Failure) Unwrap() error { return f.Err }

// AsFailure extracts a *Failure, if present.
func AsFailure(err error) (*Failure, bool) {
	var f *Failure
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}

// NodeState records what happened to a node.
type NodeState struct {
	Name      string
	Key       string
	Status    Status
	Deps      []string
	Result    Result
	Err       error
	BlockedBy string // name of the first failing ancestor, if blocked
	Recovered bool
}

// Report is the deterministic outcome of one graph run.
type Report struct {
	States []NodeState
	Order  []string // completion order, independent branches included
}

// State returns a node's state by name.
func (r *Report) State(name string) (NodeState, bool) {
	for _, s := range r.States {
		if s.Name == name {
			return s, true
		}
	}
	return NodeState{}, false
}

// Failed returns the first failure in node-declaration order, if any.
func (r *Report) Failed() (NodeState, bool) {
	for _, s := range r.States {
		if s.Status == StatusFailed {
			return s, true
		}
	}
	return NodeState{}, false
}

// BlockedBy returns the first blocking dependency name, or "none".
func (r *Report) BlockedBy() string {
	for _, s := range r.States {
		if s.Status == StatusBlocked || s.Status == StatusFailed {
			if s.BlockedBy != "" {
				return s.BlockedBy
			}
			return s.Name
		}
	}
	return "none"
}

// Recovered counts reused verified results.
func (r *Report) Recovered() int {
	n := 0
	for _, s := range r.States {
		if s.Status == StatusRecovered || s.Recovered {
			n++
		}
	}
	return n
}

// Graph is a validated, immutable node set.
type Graph struct {
	nodes map[string]*Node
	order []string
}

// Build validates the graph and computes a topological order.
func Build(nodes []Node) (*Graph, error) {
	g := &Graph{nodes: make(map[string]*Node, len(nodes))}
	for i := range nodes {
		n := nodes[i]
		if n.Name == "" {
			return nil, errors.New("dag: node with empty name")
		}
		if _, dup := g.nodes[n.Name]; dup {
			return nil, fmt.Errorf("dag: duplicate node %q", n.Name)
		}
		if n.Run == nil {
			return nil, fmt.Errorf("dag: node %q has no Run", n.Name)
		}
		cp := n
		g.nodes[n.Name] = &cp
	}
	// Validate edges and detect cycles with Kahn's algorithm, ordering by
	// declaration position so equivalent graphs always produce the same order.
	indeg := make(map[string]int, len(g.nodes))
	for name, n := range g.nodes {
		for _, d := range n.DependsOn {
			if _, ok := g.nodes[d]; !ok {
				return nil, fmt.Errorf("dag: node %q depends on unknown node %q", name, d)
			}
			indeg[name]++
		}
	}
	for name := range g.nodes {
		if _, ok := indeg[name]; !ok {
			indeg[name] = 0
		}
	}
	queue := make([]string, 0, len(g.nodes))
	pos := make(map[string]int, len(g.nodes))
	for i, n := range nodes {
		pos[n.Name] = i
	}
	for _, n := range nodes {
		if indeg[n.Name] == 0 {
			queue = append(queue, n.Name)
		}
	}
	for len(queue) > 0 {
		sortByName(queue, pos)
		name := queue[0]
		queue = queue[1:]
		g.order = append(g.order, name)
		for _, n := range nodes {
			for _, d := range n.DependsOn {
				if d == name {
					indeg[n.Name]--
					if indeg[n.Name] == 0 {
						queue = append(queue, n.Name)
					}
				}
			}
		}
	}
	if len(g.order) != len(g.nodes) {
		return nil, errors.New("dag: dependency cycle detected")
	}
	return g, nil
}

func sortByName(names []string, pos map[string]int) {
	sort.Slice(names, func(i, j int) bool { return pos[names[i]] < pos[names[j]] })
}

// Node returns a node by name.
func (g *Graph) Node(name string) (*Node, bool) {
	n, ok := g.nodes[name]
	return n, ok
}

// KeyFor computes a node's idempotency key from its kind, parameters, own
// input hashes and the hashes of its dependencies' results.
func (g *Graph) KeyFor(name string, deps map[string]Result) (string, error) {
	n, ok := g.nodes[name]
	if !ok {
		return "", fmt.Errorf("dag: unknown node %q", name)
	}
	return KeyOf(n, deps), nil
}

// Runner executes a graph. Workers bounds parallelism; 0 means sequential.
type Runner struct {
	Cache   Cache
	Workers int
	// AllowRecovery globally enables cache reuse at the graph level.
	AllowRecovery bool
	// Policy governs retries of transient node failures; the zero value
	// performs a single attempt.
	Policy Policy
	// OnCommit, when non-nil, is invoked after each successful commit. It
	// supports crash-recovery tests without changing node code.
	OnCommit func(name, key string)
}

// Run executes the graph with up to Workers concurrent nodes. It returns a
// report in topological order regardless of completion timing, so independent
// branches finishing early never change the printed outcome.
func (r *Runner) Run(ctx context.Context, g *Graph) (*Report, error) {
	if r.Workers <= 1 {
		rep, err := r.RunSequential(ctx, g)
		if rep == nil {
			rep = &Report{}
		}
		return rep, err
	}
	workers := r.Workers
	if workers > 16 {
		workers = 16
	}
	states := make(map[string]*NodeState, len(g.nodes))
	results := make(map[string]Result)
	for _, name := range g.order {
		states[name] = &NodeState{Name: name, Deps: append([]string(nil), g.nodes[name].DependsOn...), Status: StatusPending}
	}

	type completion struct {
		name   string
		res    Result
		err    error
		reused bool
	}
	doneCh := make(chan completion, len(g.nodes))
	inflight := 0
	finished := 0

	resolveBlocked := func(name, by string) {
		st := states[name]
		st.Status = StatusBlocked
		st.BlockedBy = by
		finished++
	}

	for finished < len(g.nodes) {
		// Launch every node whose dependencies are satisfied and whose slot
		// budget allows. Nodes with failed or blocked dependencies become
		// blocked without consuming a worker.
		launched := false
		for _, name := range g.order {
			st := states[name]
			if st.Status != StatusPending {
				continue
			}
			ready, blockedBy := true, ""
			for _, d := range st.Deps {
				ds := states[d]
				if ds.Status == StatusFailed || ds.Status == StatusBlocked {
					ready = false
					blockedBy = ds.BlockedBy
					if blockedBy == "" {
						blockedBy = d
					}
					break
				}
				if ds.Status != StatusDone && ds.Status != StatusRecovered {
					ready = false
					break
				}
			}
			if !ready {
				if blockedBy != "" {
					resolveBlocked(name, blockedBy)
					launched = true
				}
				continue
			}
			if inflight >= workers {
				continue
			}
			st.Status = StatusRunning
			inflight++
			launched = true
			depsRes := make(map[string]Result, len(st.Deps))
			for _, d := range st.Deps {
				depsRes[d] = results[d]
			}
			node := g.nodes[name]
			key := KeyOf(node, depsRes)
			st.Key = key
			go func(name string, node *Node, key string, depsRes map[string]Result) {
				res, reused, err := r.execute(ctx, node, key, depsRes)
				doneCh <- completion{name: name, res: res, err: err, reused: reused}
			}(name, g.nodes[name], key, depsRes)
		}

		if inflight == 0 {
			// Nothing is running. If every pending node lacks a blocked/failed
			// ancestor but no progress was made, the graph is deadlocked; that
			// would be a bug in Build, but fail loudly rather than spin.
			if !launched {
				return nil, fmt.Errorf("dag: scheduler stalled with %d nodes unfinished", len(g.nodes)-finished)
			}
			continue
		}

		select {
		case <-ctx.Done():
			// Wait for in-flight nodes to observe cancellation before
			// returning, so no goroutine writes to a report we hand back.
			for inflight > 0 {
				<-doneCh
				inflight--
			}
			return r.report(g, states), ctx.Err()
		case c := <-doneCh:
			inflight--
			st := states[c.name]
			st.Result = c.res
			if c.reused {
				st.Status = StatusRecovered
				st.Recovered = true
			} else if c.err != nil {
				st.Status = StatusFailed
				st.Err = c.err
			} else {
				st.Status = StatusDone
			}
			if c.err == nil {
				results[c.name] = c.res
			}
			finished++
		}
	}
	return r.report(g, states), ctx.Err()
}

// execute runs one node, consulting the cache first and committing
// idempotently. reused reports a verified cache hit.
func (r *Runner) execute(ctx context.Context, node *Node, key string, depsRes map[string]Result) (res Result, reused bool, err error) {
	if r.Cache != nil && (r.AllowRecovery || node.AllowRecovery) {
		if cached, ok := r.Cache.Get(key); ok {
			if node.Verify == nil || node.Verify(ctx, key, cached) == nil {
				return cached, true, nil
			}
			_ = r.Cache.Put(key, Result{})
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, false, err
	}
	var runErr error
	attempts := r.Policy.Attempts
	if attempts <= 0 {
		attempts = 1
	}
	pol := r.Policy
	pol.Attempts = attempts
	runErr = Retry(ctx, pol, func(ctx context.Context, attempt int) error {
		var err error
		res, err = node.Run(ctx, key, depsRes)
		return err
	})
	if runErr != nil {
		return Result{}, false, runErr
	}
	if node.Commit != nil {
		// Commit is idempotent: committing an already-committed key is a
		// no-op and may not change the committed artifact.
		if err := node.Commit(ctx, key, res); err != nil {
			return Result{}, false, err
		}
	}
	if r.Cache != nil {
		_ = r.Cache.Put(key, res)
	}
	if r.OnCommit != nil {
		r.OnCommit(node.Name, key)
	}
	return res, false, nil
}

func (r *Runner) report(g *Graph, states map[string]*NodeState) *Report {
	rep := &Report{}
	for _, name := range g.order {
		rep.States = append(rep.States, *states[name])
		rep.Order = append(rep.Order, name)
	}
	return rep
}

// RunSequential executes nodes one at a time in topological order. It is the
// reference behaviour used by tests and for reproducible single-thread runs.
func (r *Runner) RunSequential(ctx context.Context, g *Graph) (*Report, error) {
	rep := &Report{}
	results := make(map[string]Result)
	states := make(map[string]NodeState, len(g.nodes))
	for _, name := range g.order {
		n := g.nodes[name]
		st := NodeState{Name: name, Deps: append([]string(nil), n.DependsOn...), Status: StatusPending}
		blocked := ""
		for _, d := range n.DependsOn {
			ds := states[d]
			if ds.Status == StatusFailed || ds.Status == StatusBlocked {
				st.Status = StatusBlocked
				if ds.BlockedBy != "" {
					blocked = ds.BlockedBy
				} else {
					blocked = d
				}
				break
			}
			if ds.Status != StatusDone && ds.Status != StatusRecovered {
				return rep, fmt.Errorf("dag: %q scheduled before dependency %q", name, d)
			}
		}
		if blocked != "" {
			st.BlockedBy = blocked
			states[name] = st
			rep.States = append(rep.States, st)
			rep.Order = append(rep.Order, name)
			continue
		}
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
		key := KeyOf(n, results)
		st.Key = key
		if r.Cache != nil && (r.AllowRecovery || n.AllowRecovery) {
			if cached, ok := r.Cache.Get(key); ok {
				if n.Verify == nil || n.Verify(ctx, key, cached) == nil {
					st.Status = StatusRecovered
					st.Recovered = true
					st.Result = cached
					results[name] = cached
					states[name] = st
					rep.States = append(rep.States, st)
					rep.Order = append(rep.Order, name)
					continue
				}
			}
		}
		var res Result
		err := Retry(ctx, r.Policy, func(ctx context.Context, attempt int) error {
			var runErr error
			res, runErr = n.Run(ctx, key, results)
			return runErr
		})
		if err != nil {
			st.Status = StatusFailed
			st.Err = err
			states[name] = st
			rep.States = append(rep.States, st)
			rep.Order = append(rep.Order, name)
			continue
		}
		if n.Commit != nil {
			if err := n.Commit(ctx, key, res); err != nil {
				st.Status = StatusFailed
				st.Err = err
				states[name] = st
				rep.States = append(rep.States, st)
				rep.Order = append(rep.Order, name)
				continue
			}
		}
		if r.Cache != nil {
			_ = r.Cache.Put(key, res)
		}
		st.Status = StatusDone
		st.Result = res
		results[name] = res
		states[name] = st
		rep.States = append(rep.States, st)
		rep.Order = append(rep.Order, name)
	}
	return rep, ctx.Err()
}

func posOf(order []string) map[string]int {
	m := make(map[string]int, len(order))
	for i, n := range order {
		m[n] = i
	}
	return m
}
