// Package pipeline executes the explicit track/processing graph in dependency
// order and provides checkpointed, idempotent resume.
package pipeline

import (
	"fmt"
	"sync"

	"continuity/internal/timeline"
)

// Context carries shared run state between steps.
type Context struct {
	mu     sync.Mutex
	values map[string]interface{}
}

// NewContext returns an empty step context.
func NewContext() *Context { return &Context{values: map[string]interface{}{}} }

// Set stores a value produced by a step.
func (c *Context) Set(k string, v interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[k] = v
}

// Get retrieves a value produced by an earlier step.
func (c *Context) Get(k string) (interface{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.values[k]
	return v, ok
}

// StepFunc is one processing step.
type StepFunc func(ctx *Context) error

// Step binds a graph node to its implementation and schema version.
type Step struct {
	Node    timeline.Node
	Version int
	Fn      StepFunc
}

// Status is the terminal state of a step in one run.
type Status string

const (
	StatusOK      Status = "ok"
	StatusCached  Status = "cached" // result reused from checkpoint
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped" // dependency did not succeed
)

// Result is the outcome of one step.
type Result struct {
	Status Status
	Err    error
}

// Checkpointer records completed steps so a rerun can resume.
type Checkpointer interface {
	Done(id string) bool
	Record(id string) error
}

// Pipeline is a validated, topologically ordered step graph.
type Pipeline struct {
	steps map[string]*Step
	order []string
}

// New validates the graph (unknown deps, cycles) and computes a deterministic
// topological order following declaration order where possible.
func New(steps ...*Step) (*Pipeline, error) {
	p := &Pipeline{steps: map[string]*Step{}}
	for _, s := range steps {
		if s.Node.ID == "" {
			return nil, fmt.Errorf("pipeline: step with empty id")
		}
		if _, dup := p.steps[s.Node.ID]; dup {
			return nil, fmt.Errorf("pipeline: duplicate step %q", s.Node.ID)
		}
		p.steps[s.Node.ID] = s
	}
	for _, s := range steps {
		for _, d := range s.Node.Deps {
			if _, ok := p.steps[d]; !ok {
				return nil, fmt.Errorf("pipeline: step %q depends on unknown step %q", s.Node.ID, d)
			}
		}
	}
	indeg := map[string]int{}
	for _, s := range steps {
		indeg[s.Node.ID] = len(s.Node.Deps)
	}
	var queue []string
	for _, s := range steps {
		if indeg[s.Node.ID] == 0 {
			queue = append(queue, s.Node.ID)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		p.order = append(p.order, id)
		for _, s := range steps {
			for _, d := range s.Node.Deps {
				if d == id {
					indeg[s.Node.ID]--
					if indeg[s.Node.ID] == 0 {
						queue = append(queue, s.Node.ID)
					}
				}
			}
		}
	}
	if len(p.order) != len(steps) {
		return nil, fmt.Errorf("pipeline: dependency cycle detected")
	}
	return p, nil
}

// Order returns the topological execution order.
func (p *Pipeline) Order() []string { return append([]string(nil), p.order...) }

// Versions maps step id to schema version, in execution order.
func (p *Pipeline) Versions() map[string]int {
	m := map[string]int{}
	for _, id := range p.order {
		m[id] = p.steps[id].Version
	}
	return m
}

// Run executes steps in dependency order. A step whose dependencies did not
// all succeed is skipped and never produces a success marker. With a
// Checkpointer, completed steps are recorded and reused on rerun.
func (p *Pipeline) Run(ctx *Context, cp Checkpointer) map[string]Result {
	res := map[string]Result{}
	for _, id := range p.order {
		s := p.steps[id]
		blocked := false
		for _, d := range s.Node.Deps {
			if r := res[d]; r.Status != StatusOK && r.Status != StatusCached {
				blocked = true
			}
		}
		if blocked {
			res[id] = Result{Status: StatusSkipped}
			continue
		}
		if cp != nil && cp.Done(id) {
			res[id] = Result{Status: StatusCached}
			continue
		}
		if err := s.Fn(ctx); err != nil {
			res[id] = Result{Status: StatusFailed, Err: err}
			continue
		}
		if cp != nil {
			if err := cp.Record(id); err != nil {
				res[id] = Result{Status: StatusFailed, Err: err}
				continue
			}
		}
		res[id] = Result{Status: StatusOK}
	}
	return res
}
