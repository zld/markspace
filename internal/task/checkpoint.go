package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Checkpoint persists node states and results so a crashed run can resume
// without re-executing unaffected nodes.
type Checkpoint struct {
	Path  string              `json:"-"`
	Nodes map[string]*NodeState `json:"nodes"`
}

// LoadCheckpoint reads a checkpoint file; a missing file yields an empty
// checkpoint (first run).
func LoadCheckpoint(path string) (*Checkpoint, error) {
	c := &Checkpoint{Path: path, Nodes: make(map[string]*NodeState)}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
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
