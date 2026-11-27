// Package state persists pipeline checkpoints so an interrupted run
// can resume without re-committing already-committed events.
package state

import (
	"encoding/json"
	"errors"
	"os"
)

// Checkpoint captures the resumable pipeline state.
type Checkpoint struct {
	Phase           string `json:"phase"`
	CommittedEvents int    `json:"committed_events"`
	LastTimestampNs int64  `json:"last_timestamp_ns"`
	InputOffset     int64  `json:"input_offset"`
	Digest          uint64 `json:"digest"`
}

// Save writes the checkpoint atomically (temp file + rename).
func Save(path string, c Checkpoint) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads a checkpoint. The second return value is false when no
// checkpoint file exists.
func Load(path string) (Checkpoint, bool, error) {
	var c Checkpoint
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, false, err
	}
	return c, true, nil
}
