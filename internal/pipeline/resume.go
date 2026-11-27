package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Checkpoint persists resume state on disk. It is bound to a specific input
// hash, configuration hash and set of step versions; any mismatch refuses
// reuse of the old checkpoint.
type Checkpoint struct {
	InputHash  string         `json:"inputHash"`
	ConfigHash string         `json:"configHash"`
	Versions   map[string]int `json:"versions"`
	Completed  []string       `json:"completed"`

	path string
}

// Refusal errors: a rerun with any of these must abort (exit code 1).
var (
	ErrInputChanged   = errors.New("pipeline: input changed; refusing to reuse checkpoint")
	ErrConfigChanged  = errors.New("pipeline: configuration changed; refusing to reuse checkpoint")
	ErrVersionChanged = errors.New("pipeline: step version changed; refusing to reuse checkpoint")
)

// CheckpointPath is the checkpoint file location inside an output directory.
func CheckpointPath(dir string) string { return filepath.Join(dir, "checkpoint.json") }

// LoadCheckpoint reads the checkpoint in dir; a missing file yields a fresh,
// unbound checkpoint.
func LoadCheckpoint(dir string) (*Checkpoint, error) {
	p := CheckpointPath(dir)
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &Checkpoint{path: p}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Checkpoint
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("pipeline: corrupt checkpoint %s: %w", p, err)
	}
	c.path = p
	return &c, nil
}

// Bind validates the checkpoint against the current run and rebinds it. A
// bound checkpoint whose input, config or step versions differ is rejected.
func (c *Checkpoint) Bind(inputHash, configHash string, versions map[string]int) error {
	if c.InputHash != "" {
		if c.InputHash != inputHash {
			return ErrInputChanged
		}
		if c.ConfigHash != configHash {
			return ErrConfigChanged
		}
		for id, v := range c.Versions {
			if versions[id] != v {
				return fmt.Errorf("%w: step %s", ErrVersionChanged, id)
			}
		}
	}
	c.InputHash = inputHash
	c.ConfigHash = configHash
	c.Versions = versions
	return nil
}

// Done reports whether a step completed in a previous run.
func (c *Checkpoint) Done(id string) bool {
	for _, x := range c.Completed {
		if x == id {
			return true
		}
	}
	return false
}

// Record marks a step complete and persists the checkpoint atomically.
func (c *Checkpoint) Record(id string) error {
	if !c.Done(id) {
		c.Completed = append(c.Completed, id)
	}
	return c.Save()
}

// Save writes the checkpoint atomically (temp file + rename).
func (c *Checkpoint) Save() error {
	if c.path == "" {
		return errors.New("pipeline: checkpoint not bound to a path")
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
