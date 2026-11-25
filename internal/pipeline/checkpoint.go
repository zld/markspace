package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"regress-kernel/internal/media"
)

// Checkpoint is the durable marker written at each successful stage boundary.
// It carries the task-level input digest so a rerun can prove it resumes
// against the same inputs rather than silently processing a changed program.
type Checkpoint struct {
	TaskKey      string `json:"task_key"`
	Stage        Stage  `json:"stage"`
	StageOrdinal int    `json:"stage_ordinal"`
	InputDigest  string `json:"input_digest"`
	Status       string `json:"status"` // "done"
	// StageSummary keeps small, deterministic stage outputs next to the marker.
	StageSummary map[string]interface{} `json:"stage_summary,omitempty"`
}

func (w *Workspace) checkpointPath(key string, stage Stage) (string, error) {
	return w.SafeJoin("checkpoints", sanitizeKey(key), string(stage)+".json")
}

// InputDigest hashes the canonical JSON of the task input summary; the same
// value is stamped into every stage checkpoint of that task.
func InputDigest(v interface{}) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// WriteCheckpoint persists the successful completion of one stage.
func (w *Workspace) WriteCheckpoint(key string, stage Stage,
	inputDigest string, summary map[string]interface{}) (*Checkpoint, error) {
	cp := &Checkpoint{
		TaskKey:      key,
		Stage:        stage,
		StageOrdinal: StageIndex(stage),
		InputDigest:  inputDigest,
		Status:       "done",
		StageSummary: summary,
	}
	path, err := w.checkpointPath(key, stage)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return cp, nil
}

// LastCheckpoint returns the highest completed valid checkpoint, or nil when
// the task must start from ingest. A digest mismatch invalidates every later
// checkpoint because the inputs no longer match what was processed.
func (w *Workspace) LastCheckpoint(key string, expectedDigest string) (*Checkpoint, error) {
	var best *Checkpoint
	for _, stage := range Stages {
		path, err := w.checkpointPath(key, stage)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			break // stages are contiguous; first gap ends the chain
		}
		if err != nil {
			return nil, err
		}
		var cp Checkpoint
		if err := json.Unmarshal(data, &cp); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", media.ErrCheckpoint, stage, err)
		}
		if cp.Status != "done" || cp.Stage != stage {
			return nil, fmt.Errorf("%w: corrupt marker at %s", media.ErrCheckpoint, stage)
		}
		if cp.InputDigest != expectedDigest {
			break // changed inputs: replay from the first unverified stage
		}
		best = &cp
	}
	return best, nil
}

// CheckpointExists reports whether one stage marker is already present.
func (w *Workspace) CheckpointExists(key string, stage Stage) (bool, error) {
	path, err := w.checkpointPath(key, stage)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}
