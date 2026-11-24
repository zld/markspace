// Package task schedules the analysis DAG with idempotent node keys,
// checkpointed resume and first-blocking-dependency reporting.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Category classifies node failures for upstream reporting.
type Category string

const (
	CatInputCorrupt Category = "input-corrupt"
	CatNoAnchor     Category = "no-anchor"
	CatInternal     Category = "internal"
)

// Error is a categorized node failure.
type Error struct {
	Category Category
	Msg      string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Category, e.Msg) }

// Node is one unit of work in the DAG.
type Node struct {
	Name    string
	Deps    []string
	Version string
	// Input is the canonical digest material (input summary + params).
	Input []byte
	// Run executes the node using resolved dependency results.
	Run func(deps map[string]any) (any, error)
	// Decode restores a persisted result from the checkpoint.
	Decode func(raw json.RawMessage) (any, error)
}

// NodeState is the persisted outcome of one node.
type NodeState struct {
	Key      string          `json:"key"`
	Status   string          `json:"status"` // done | failed | blocked
	Digest   string          `json:"digest,omitempty"`
	Retries  int             `json:"retries"`
	Category string          `json:"category,omitempty"`
	ErrMsg   string          `json:"error,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
}

// Report summarizes one scheduler run.
type Report struct {
	States          map[string]*NodeState
	Results         map[string]any
	Reused          int // nodes served from checkpoint
	Executed        int
	FirstBlocked    string   // first failed node blocking others ("" if none)
	BlockedCategory Category // its error category
}

// Scheduler executes nodes in dependency order with idempotency keys.
type Scheduler struct {
	nodes    map[string]*Node
	order    []string // topo order
	ckpt     *Checkpoint
	inFlight map[string]bool
	mu       sync.Mutex
}

// NewScheduler builds a scheduler; nodes must be acyclic.
func NewScheduler(nodes []*Node, ckpt *Checkpoint) (*Scheduler, error) {
	s := &Scheduler{
		nodes:    make(map[string]*Node),
		ckpt:     ckpt,
		inFlight: make(map[string]bool),
	}
	for _, n := range nodes {
		if _, dup := s.nodes[n.Name]; dup {
			return nil, fmt.Errorf("task: duplicate node %q", n.Name)
		}
		s.nodes[n.Name] = n
	}
	order, err := topoSort(nodes)
	if err != nil {
		return nil, err
	}
	s.order = order
	return s, nil
}

func topoSort(nodes []*Node) ([]string, error) {
	deps := make(map[string][]string)
	for _, n := range nodes {
		deps[n.Name] = n.Deps
	}
	var order []string
	done := make(map[string]bool)
	for len(order) < len(nodes) {
		progress := false
		for _, n := range nodes {
			if done[n.Name] {
				continue
			}
			ready := true
			for _, d := range n.Deps {
				if !done[d] {
					ready = false
					break
				}
			}
			if ready {
				order = append(order, n.Name)
				done[n.Name] = true
				progress = true
			}
		}
		if !progress {
			return nil, fmt.Errorf("task: dependency cycle or missing dependency")
		}
	}
	return order, nil
}

// key computes the idempotency key: name, version, input digest, dep keys.
func (s *Scheduler) key(n *Node, states map[string]*NodeState) string {
	h := sha256.New()
	h.Write([]byte(n.Name))
	h.Write([]byte{0})
	h.Write([]byte(n.Version))
	h.Write([]byte{0})
	h.Write(n.Input)
	deps := append([]string(nil), n.Deps...)
	sort.Strings(deps)
	for _, d := range deps {
		h.Write([]byte{0})
		h.Write([]byte(states[d].Key))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func digestOf(v any) (string, json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), raw, nil
}

// trySubmit claims an idempotency key; false means it is already in flight.
func (s *Scheduler) trySubmit(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight[key] {
		return false
	}
	s.inFlight[key] = true
	return true
}

func (s *Scheduler) release(key string) {
	s.mu.Lock()
	delete(s.inFlight, key)
	s.mu.Unlock()
}

// Run executes the DAG in topological order. Nodes whose idempotency key
// matches a done checkpoint entry are reused without re-execution.
func (s *Scheduler) Run() *Report {
	rep := &Report{
		States:  make(map[string]*NodeState),
		Results: make(map[string]any),
	}
	states := make(map[string]*NodeState)
	for _, name := range s.order {
		n := s.nodes[name]
		depResults := make(map[string]any, len(n.Deps))
		blocked := false
		for _, d := range n.Deps {
			if states[d].Status != "done" {
				blocked = true
				break
			}
			depResults[d] = rep.Results[d]
		}
		if blocked {
			st := &NodeState{Status: "blocked", ErrMsg: "upstream dependency not done"}
			states[name] = st
			rep.States[name] = st
			continue
		}
		key := s.key(n, states)
		if !s.trySubmit(key) {
			st := &NodeState{Key: key, Status: "failed", Category: string(CatInternal),
				ErrMsg: "duplicate idempotency key submission"}
			states[name] = st
			rep.States[name] = st
			if rep.FirstBlocked == "" {
				rep.FirstBlocked = name
				rep.BlockedCategory = CatInternal
			}
			continue
		}
		st := s.execOne(n, key, depResults, rep)
		s.release(key)
		states[name] = st
		rep.States[name] = st
	}
	if s.ckpt != nil {
		for name, st := range states {
			s.ckpt.Nodes[name] = st
		}
		_ = s.ckpt.Save()
	}
	return rep
}

func (s *Scheduler) execOne(n *Node, key string, deps map[string]any, rep *Report) *NodeState {
	attempts := 1
	if s.ckpt != nil {
		if prev, ok := s.ckpt.Nodes[n.Name]; ok && prev.Key == key {
			attempts = prev.Retries + 1
			if prev.Status == "done" {
				if res, err := n.Decode(prev.Result); err == nil {
					rep.Results[n.Name] = res
					rep.Reused++
					st := *prev
					return &st
				}
			}
		}
	}
	res, err := n.Run(deps)
	if err != nil {
		cat := CatInternal
		if ce, ok := err.(*Error); ok {
			cat = ce.Category
		}
		if rep.FirstBlocked == "" {
			rep.FirstBlocked = n.Name
			rep.BlockedCategory = cat
		}
		rep.Executed++
		return &NodeState{Key: key, Status: "failed", Retries: attempts,
			Category: string(cat), ErrMsg: err.Error()}
	}
	digest, raw, derr := digestOf(res)
	if derr != nil {
		rep.Executed++
		return &NodeState{Key: key, Status: "failed", Retries: attempts,
			Category: string(CatInternal), ErrMsg: derr.Error()}
	}
	rep.Results[n.Name] = res
	rep.Executed++
	return &NodeState{Key: key, Status: "done", Digest: digest, Retries: attempts, Result: raw}
}
