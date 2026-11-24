package dag

import (
	"context"
	"errors"
	"sort"

	"github.com/zld/avpipe/internal/fault"
)

// Status is a node's terminal or scheduling state.
type Status string

const (
	StatusPending  Status = "pending"
	StatusRunning  Status = "running"
	StatusSuccess  Status = "success"
	StatusFailed   Status = "failed"
	StatusSkipped  Status = "skipped"
	StatusCanceled Status = "canceled"
)

// Node is one deterministic DAG unit.
type Node struct {
	Name         string
	Dependencies []string
	MaxAttempts  int
	Run          func(ctx context.Context) error
}

// NodeResult records the outcome of one node.
type NodeResult struct {
	Name     string `json:"name"`
	Status   Status `json:"status"`
	Attempts int    `json:"attempts"`
	Error    string `json:"error,omitempty"`
}

// Report contains node statuses and blocking dependency information.
type Report struct {
	Order                  []string               `json:"order"`
	Results                map[string]*NodeResult `json:"results"`
	FirstFailedNode        string                 `json:"first_failed_node,omitempty"`
	FirstBlockedNode       string                 `json:"first_blocked_node,omitempty"`
	FirstBlockedDependency string                 `json:"first_blocked_dependency,omitempty"`
	Canceled               bool                   `json:"canceled"`
}

// FirstBlockingDependency returns the root failed dependency name, or "".
func (r *Report) FirstBlockingDependency() string {
	if r == nil {
		return ""
	}
	return r.FirstBlockedDependency
}

// Scheduler executes a validated DAG in deterministic topological order.
type Scheduler struct {
	nodes map[string]Node
	names []string
}

// NewScheduler validates names, dependencies, and cycles.
func NewScheduler(nodes ...Node) (*Scheduler, error) {
	s := &Scheduler{nodes: make(map[string]Node, len(nodes))}
	for _, n := range nodes {
		if n.Name == "" {
			return nil, fault.New(fault.KindInvalid, "DAG node name is empty")
		}
		if _, dup := s.nodes[n.Name]; dup {
			return nil, fault.New(fault.KindInvalid, "duplicate DAG node %q", n.Name)
		}
		if n.Run == nil {
			return nil, fault.New(fault.KindInvalid, "DAG node %q has no Run function", n.Name)
		}
		s.nodes[n.Name] = n
		s.names = append(s.names, n.Name)
	}
	for _, name := range s.names {
		for _, dep := range s.nodes[name].Dependencies {
			if _, ok := s.nodes[dep]; !ok {
				return nil, fault.New(fault.KindDependency, "node %q depends on unknown node %q", name, dep)
			}
		}
	}
	order, err := s.topologicalOrder()
	if err != nil {
		return nil, err
	}
	// names remain insertion order for "first" reporting; order is execution.
	s.names = append([]string(nil), order...)
	return s, nil
}

// Run executes nodes whose dependencies succeeded.
func (s *Scheduler) Run(ctx context.Context) *Report {
	rep := &Report{Order: append([]string(nil), s.names...), Results: make(map[string]*NodeResult, len(s.names))}
	for _, name := range s.names {
		rep.Results[name] = &NodeResult{Name: name, Status: StatusPending}
	}

	for _, name := range s.names {
		res := rep.Results[name]
		if ctx.Err() != nil {
			rep.Canceled = true
			markRemaining(rep, name, StatusCanceled)
			break
		}

		node := s.nodes[name]
		if dep := firstUnsuccessfulDependency(node, rep); dep != "" {
			res.Status = StatusSkipped
			res.Error = "blocked by dependency " + dep
			continue
		}

		res.Status = StatusRunning
		attempts, err := Retry(ctx, nil, "", Policy{MaxAttempts: maxInt(1, node.MaxAttempts)}, func(int) error {
			return node.Run(ctx)
		})
		res.Attempts = attempts
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				res.Status = StatusCanceled
				rep.Canceled = true
				markRemaining(rep, nextName(s.names, name), StatusCanceled)
				break
			}
			res.Status = StatusFailed
			res.Error = err.Error()
			if rep.FirstFailedNode == "" {
				rep.FirstFailedNode = name
			}
			continue
		}
		res.Status = StatusSuccess
	}

	s.fillBlocked(rep)
	return rep
}

// Successful reports whether every node succeeded.
func (r *Report) Successful() bool {
	for _, name := range r.Order {
		if r.Results[name].Status != StatusSuccess {
			return false
		}
	}
	return !r.Canceled
}

func (s *Scheduler) topologicalOrder() ([]string, error) {
	indegree := make(map[string]int, len(s.nodes))
	children := make(map[string][]string, len(s.nodes))
	for name, n := range s.nodes {
		indegree[name] = len(n.Dependencies)
		for _, dep := range n.Dependencies {
			children[dep] = append(children[dep], name)
		}
	}
	// Deterministic Kahn ordering independent of map iteration.
	var ready []string
	for name, d := range indegree {
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
		next := append([]string(nil), children[name]...)
		sort.Strings(next)
		for _, child := range next {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
			}
		}
		sort.Strings(ready)
	}
	if len(order) != len(s.nodes) {
		return nil, fault.New(fault.KindDependency, "dependency cycle detected")
	}
	return order, nil
}

func firstUnsuccessfulDependency(n Node, rep *Report) string {
	for _, dep := range n.Dependencies {
		r := rep.Results[dep]
		if r == nil || r.Status != StatusSuccess {
			return dep
		}
	}
	return ""
}

func (s *Scheduler) fillBlocked(rep *Report) {
	// Execution order is topological; the first skipped node gives the first
	// blocking dependency in deterministic graph order.
	for _, name := range s.names {
		res := rep.Results[name]
		if res == nil || res.Status != StatusSkipped {
			continue
		}
		node := s.nodes[name]
		for _, dep := range node.Dependencies {
			if dr := rep.Results[dep]; dr != nil && dr.Status == StatusFailed {
				rep.FirstBlockedNode = name
				rep.FirstBlockedDependency = dep
				return
			}
		}
		for _, dep := range node.Dependencies {
			if dr := rep.Results[dep]; dr != nil && dr.Status != StatusSuccess {
				rep.FirstBlockedNode = name
				rep.FirstBlockedDependency = dep
				return
			}
		}
	}
	// If the failed node itself is the terminal requested dependency, report
	// it as the first blocking dependency for callers.
	if rep.FirstFailedNode != "" && rep.FirstBlockedDependency == "" {
		rep.FirstBlockedDependency = rep.FirstFailedNode
	}
}

func markRemaining(rep *Report, start string, status Status) {
	started := start == ""
	for _, name := range rep.Order {
		if !started && name == start {
			started = true
		}
		if !started {
			continue
		}
		if r := rep.Results[name]; r != nil && (r.Status == StatusPending || r.Status == StatusRunning) {
			r.Status = status
		}
	}
}

func nextName(order []string, current string) string {
	for i, n := range order {
		if n == current && i+1 < len(order) {
			return order[i+1]
		}
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
