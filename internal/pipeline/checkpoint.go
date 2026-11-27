// Package pipeline orchestrates incremental, resumable media processing.
// Stage checkpoints record input/parameter/version/artifact digests after
// every stage; an interrupted run reuses only fully matching stages.
package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediakernel/internal/cache"
	"mediakernel/internal/clock"
	"mediakernel/internal/fsx"
)

// StageState is the persisted outcome of one processing stage.
type StageState struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	InputDigest    string `json:"input_digest"`
	ParamsDigest   string `json:"params_digest"`
	ArtifactDigest string `json:"artifact_digest"`
}

// Checkpoint is the full resume record for a run.
type Checkpoint struct {
	InputDigest  string       `json:"input_digest"`
	ParamsDigest string       `json:"params_digest"`
	Stages       []StageState `json:"stages"`
}

// CheckpointStore reads and atomically writes one checkpoint file.
type CheckpointStore struct {
	base string
	path string
}

// NewCheckpointStore resolves the checkpoint location inside the sandbox.
func NewCheckpointStore(base, relPath string) (*CheckpointStore, error) {
	p, err := fsx.EnsureOutput(base, relPath)
	if err != nil {
		return nil, err
	}
	return &CheckpointStore{base: base, path: p}, nil
}

// Load reads a checkpoint. A missing file yields a nil checkpoint with no
// error (fresh run); a malformed file yields a structured corruption error.
func (s *CheckpointStore) Load() (*Checkpoint, error) {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, clock.NewError("checkpoint", clock.CatCheckpoint,
			"checkpoint read failed").Wrap(err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, clock.NewError("checkpoint", clock.CatCheckpoint,
			"checkpoint json corrupt").Wrap(err)
	}
	if cp.InputDigest == "" || cp.ParamsDigest == "" {
		return nil, clock.NewError("checkpoint", clock.CatCheckpoint,
			"checkpoint missing identity digests")
	}
	for _, st := range cp.Stages {
		if st.Name == "" || st.ArtifactDigest == "" || st.Version == "" {
			return nil, clock.NewError("checkpoint", clock.CatCheckpoint,
				"stage %q has empty required field", st.Name)
		}
	}
	return &cp, nil
}

// Save atomically replaces the checkpoint after a stage completes.
func (s *CheckpointStore) Save(cp *Checkpoint) error {
	if cp.InputDigest == "" || cp.ParamsDigest == "" {
		return clock.NewError("checkpoint", clock.CatCheckpoint,
			"refusing to save checkpoint without identity digests")
	}
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return clock.NewError("checkpoint", clock.CatCheckpoint,
			"checkpoint marshal failed").Wrap(err)
	}
	return atomicFileWrite(s.path, raw, 0o644)
}

// ReusableStages returns the longest prefix of expected stages whose
// version and identity digests match. When an expected stage carries a
// concrete artifact digest it must also match; an empty expected digest
// means "stage present and recorded" (heavy artifacts are re-verified
// separately through the cache). A later mismatch stops reuse at that
// boundary; stages are only reused as an uninterrupted completed prefix.
func (cp *Checkpoint) ReusableStages(expected []StageState) int {
	if cp == nil {
		return 0
	}
	n := 0
	for i, want := range expected {
		if i >= len(cp.Stages) {
			break
		}
		got := cp.Stages[i]
		if got.Name != want.Name || got.Version != want.Version ||
			got.InputDigest != want.InputDigest ||
			got.ParamsDigest != want.ParamsDigest ||
			got.ArtifactDigest == "" {
			break
		}
		if want.ArtifactDigest != "" && got.ArtifactDigest != want.ArtifactDigest {
			break
		}
		n++
	}
	return n
}

// Remove deletes a corrupt checkpoint so the next run starts cleanly.
func (s *CheckpointStore) Remove() error {
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return clock.NewError("checkpoint", clock.CatCheckpoint,
			"checkpoint remove failed").Wrap(err)
	}
	return nil
}

// ParamsDigest renders parameters to a stable hash for checkpoint matching.
func ParamsDigest(p cache.Params) string {
	return cache.DigestBytes(mustJSON(p))
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

func atomicFileWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".cp-*")
	if err != nil {
		return clock.NewError("checkpoint", clock.CatCheckpoint,
			"temp create failed").Wrap(err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return clock.NewError("checkpoint", clock.CatCheckpoint,
			"temp write failed").Wrap(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return clock.NewError("checkpoint", clock.CatCheckpoint,
			"temp chmod failed").Wrap(err)
	}
	if err := tmp.Close(); err != nil {
		return clock.NewError("checkpoint", clock.CatCheckpoint,
			"temp close failed").Wrap(err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return clock.NewError("checkpoint", clock.CatCheckpoint,
			"atomic rename failed").Wrap(err)
	}
	return nil
}
