// Package dag schedules dependent processing nodes. Each node carries an
// idempotency key derived from its input ranges, config and dependency
// result digests; checkpoints persist completed keys so an interrupted run
// resumes by reusing unaffected nodes and rerunning only what changed.
package dag

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediakernel/internal/fault"
	"mediakernel/internal/verify"
)

// ErrInterrupted is returned when the scheduler stops early on request
// (simulated crash). Rerunning Run resumes from the checkpoint.
var ErrInterrupted = errors.New("dag: interrupted")

// Status of a node after a Run attempt.
type Status string

const (
	Pending Status = "pending"
	Done    Status = "done"
	Failed  Status = "failed"
)

// Node is one unit of work. Run must be pure with respect to its declared
// inputs: it returns its output bytes, and the scheduler commits them at
// most once, which is what makes retries idempotent.
type Node struct {
	ID     string
	Deps   []string
	Ranges [][2]uint64 // input byte ranges, part of the idempotency key
	Config string      // config summary, part of the idempotency key
	Run    func() ([]byte, error)
}

type checkpointEntry struct {
	Key    string `json:"key"`
	Digest string `json:"digest"`
}

// Scheduler executes nodes in dependency order with checkpointing.
type Scheduler struct {
	Dir            string
	InterruptAfter int // stop with ErrInterrupted after this many completions; 0 = never
	Attempts       int // retry attempts per node; 0 means 1

	nodes   []*Node
	status  map[string]Status
	digests map[string]string
	ckpt    map[string]checkpointEntry

	Reused    int // nodes satisfied from checkpoint in the last Run
	completed int
}

// NewScheduler opens (or creates) the checkpoint store in dir.
func NewScheduler(dir string) (*Scheduler, error) {
	s := &Scheduler{
		Dir:     dir,
		status:  map[string]Status{},
		digests: map[string]string{},
		ckpt:    map[string]checkpointEntry{},
	}
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
	if err == nil {
		if err := json.Unmarshal(b, &s.ckpt); err != nil {
			return nil, fault.New(fault.ErrCorruptBox, fault.NA, "checkpoint unreadable: %v", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// Add registers a node. Nodes may be added in any order.
func (s *Scheduler) Add(n *Node) {
	s.nodes = append(s.nodes, n)
	if _, ok := s.status[n.ID]; !ok {
		s.status[n.ID] = Pending
	}
}

// StatusOf reports the current status of a node.
func (s *Scheduler) StatusOf(id string) Status { return s.status[id] }

// DigestOf returns the output digest of a completed node.
func (s *Scheduler) DigestOf(id string) string { return s.digests[id] }

// key computes the idempotency key of n from its inputs, config and the
// digests of its dependencies.
func (s *Scheduler) key(n *Node) string {
	h := n.ID + "|" + n.Config
	for _, r := range n.Ranges {
		h += fmt.Sprintf("|%d-%d", r[0], r[1])
	}
	deps := append([]string(nil), n.Deps...)
	sort.Strings(deps)
	for _, d := range deps {
		h += "|dep:" + d + "=" + s.digests[d]
	}
	return verify.Sum256Bytes([]byte(h))
}

// Run executes every runnable node. On node failure it returns an
// ErrDependency error naming the first blocking node and preserving the
// underlying error location; downstream nodes stay pending. On success of a
// node its checkpoint is flushed before the next node starts.
func (s *Scheduler) Run() error {
	for {
		progressed := false
		for _, n := range s.nodes {
			if s.status[n.ID] != Pending {
				continue
			}
			ready := true
			for _, d := range n.Deps {
				if s.status[d] != Done {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			progressed = true
			if err := s.runNode(n); err != nil {
				return err
			}
			if s.InterruptAfter > 0 && s.completed >= s.InterruptAfter {
				return ErrInterrupted
			}
		}
		if !progressed {
			return nil
		}
	}
}

func (s *Scheduler) runNode(n *Node) error {
	key := s.key(n)
	if ck, ok := s.ckpt[n.ID]; ok && ck.Key == key {
		s.status[n.ID] = Done
		s.digests[n.ID] = ck.Digest
		s.Reused++
		return nil
	}
	var out []byte
	err := Do(s.Attempts, func() error {
		var err error
		out, err = n.Run()
		return err
	})
	if err != nil {
		s.status[n.ID] = Failed
		return fault.New(fault.ErrDependency, fault.NA,
			"node %s blocked: %v", n.ID, err).WithNode(n.ID)
	}
	digest := verify.Sum256Bytes(out)
	if err := s.commit(n.ID, out); err != nil {
		return err
	}
	s.status[n.ID] = Done
	s.digests[n.ID] = digest
	s.ckpt[n.ID] = checkpointEntry{Key: key, Digest: digest}
	s.completed++
	return s.flush()
}

// commit writes node output atomically; retries never append twice because
// the output is a single whole-file commit.
func (s *Scheduler) commit(id string, out []byte) error {
	final := filepath.Join(s.Dir, "out", id+".bin")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

func (s *Scheduler) flush() error {
	b, err := json.MarshalIndent(s.ckpt, "", "  ")
	if err != nil {
		return err
	}
	final := filepath.Join(s.Dir, "checkpoint.json")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}
