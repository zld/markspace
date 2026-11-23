// Package batch coordinates parsing, repair and indexing with
// checkpointed recovery.
package batch

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediapack/internal/clock"
)

// ChunkRecord records one persisted index chunk and the input position at
// which it was completed, so a run can resume at any valid chunk boundary.
type ChunkRecord struct {
	Index     uint32 `json:"index"`
	Count     int    `json:"count"`      // records in this chunk
	NextInput int    `json:"next_input"` // fragment files consumed so far
	SHA256    string `json:"sha256"`
	Done      bool   `json:"done"`
}

// Checkpoint is the resumable state of a batch run.
type Checkpoint struct {
	InputHash string        `json:"input_hash"`
	Chunks    []ChunkRecord `json:"chunks"`
	Complete  bool          `json:"complete"`
}

func checkpointPath(dir string) string { return filepath.Join(dir, "checkpoint.json") }

// SaveCheckpoint atomically writes cp into dir via a temporary file and
// rename.
func SaveCheckpoint(dir string, cp *Checkpoint) error {
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := checkpointPath(dir) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return &clock.Error{Cat: clock.CatIO, Msg: "write checkpoint", Err: err}
	}
	if err := os.Rename(tmp, checkpointPath(dir)); err != nil {
		return &clock.Error{Cat: clock.CatIO, Msg: "commit checkpoint", Err: err}
	}
	return nil
}

// LoadCheckpoint reads the checkpoint in dir. It returns nil when no
// checkpoint exists or the file is unreadable or corrupt; a corrupt
// checkpoint simply restarts the run.
func LoadCheckpoint(dir string) (*Checkpoint, error) {
	data, err := os.ReadFile(checkpointPath(dir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, &clock.Error{Cat: clock.CatIO, Msg: "read checkpoint", Err: err}
	}
	var cp Checkpoint
	if json.Unmarshal(data, &cp) != nil {
		return nil, nil
	}
	return &cp, nil
}
