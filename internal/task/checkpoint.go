package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Checkpoint persists node states and results so a crashed run can resume
// without re-executing unaffected nodes. Nodes maps the latest outcome per
// node name; History retains every outcome keyed by idempotency key so that
// reverting an input to a previously-seen value can reuse the old result
// instead of recomputing it.
type Checkpoint struct {
	Path    string                `json:"-"`
	Nodes   map[string]*NodeState `json:"nodes"`
	History map[string]*NodeState `json:"history,omitempty"`
}

// Lookup returns the recorded state for an idempotency key, or nil.
func (c *Checkpoint) Lookup(key string) *NodeState {
	if c == nil || key == "" {
		return nil
	}
	return c.History[key]
}

// Record stores the latest state for a node name and archives it by key.
func (c *Checkpoint) Record(name string, st *NodeState) {
	if c.Nodes == nil {
		c.Nodes = make(map[string]*NodeState)
	}
	if c.History == nil {
		c.History = make(map[string]*NodeState)
	}
	c.Nodes[name] = st
	if st.Key != "" {
		c.History[st.Key] = st
	}
}

// LoadCheckpoint reads a checkpoint file; a missing file yields an empty
// checkpoint (first run).
func LoadCheckpoint(path string) (*Checkpoint, error) {
	c := &Checkpoint{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			c.Nodes = make(map[string]*NodeState)
			return c, nil
		}
		return nil, fmt.Errorf("task: read checkpoint: %w", err)
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("task: parse checkpoint: %w", err)
	}
	c.Path = path
	if c.Nodes == nil {
		c.Nodes = make(map[string]*NodeState)
	}
	if c.History == nil {
		c.History = make(map[string]*NodeState)
	}
	return c, nil
}

// Save writes the checkpoint atomically (temp file + rename).
func (c *Checkpoint) Save() error {
	if c.Path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.Path)
}
