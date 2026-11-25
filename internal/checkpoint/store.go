// Package checkpoint persists the pipeline's resumable position.
//
// A checkpoint records the input digests already consumed, the fragments
// that completed each stage, the current stage and the manifest version. It
// is written atomically (temp file + rename) and only after the corresponding
// output has itself been atomically persisted, so a crash can never expose a
// checkpoint ahead of the durable outputs. On resume, unfinished work is
// recomputed and completed work is reused only after digest verification.
package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Stage is the pipeline phase recorded in a checkpoint.
type Stage string

const (
	// StageScan means fragment scanning/validation has completed for the
	// listed inputs.
	StageScan Stage = "scan"
	// StageAlign means multi-representation alignment has completed.
	StageAlign Stage = "align"
	// StageManifest means the manifest has been generated and persisted.
	StageManifest Stage = "manifest"
	// StageComplete means every output, including provenance, is durable.
	StageComplete Stage = "complete"
)

// ordering lets callers verify legal stage transitions.
var ordering = map[Stage]int{
	StageScan:     1,
	StageAlign:    2,
	StageManifest: 3,
	StageComplete: 4,
}

// Checkpoint is the serialized resumable state.
type Checkpoint struct {
	// Version is incremented whenever a checkpoint is replaced.
	Version int `json:"version"`
	// Stage is the latest durably reached stage.
	Stage Stage `json:"stage"`
	// InputDigests maps repository-relative input paths to their SHA-256.
	InputDigests map[string]string `json:"input_digests"`
	// CompletedFragments lists fragment input paths that have fully passed
	// the recorded stage.
	CompletedFragments []string `json:"completed_fragments"`
	// ManifestVersion is the version of the generated manifest, empty before
	// the manifest stage.
	ManifestVersion string `json:"manifest_version"`
	// ManifestSHA256 is the digest of the persisted manifest when present.
	ManifestSHA256 string `json:"manifest_sha256"`
}

// Store loads and saves checkpoints at a fixed file path.
type Store struct {
	path string
}

// NewStore creates a store backed by path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Path returns the checkpoint file location.
func (s *Store) Path() string { return s.path }

// Exists reports whether a checkpoint file is present.
func (s *Store) Exists() bool {
	_, err := os.Stat(s.path)
	return err == nil
}

// Load reads and decodes the checkpoint.
func (s *Store) Load() (*Checkpoint, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("checkpoint: decode failed: %w", err)
	}
	if cp.InputDigests == nil {
		cp.InputDigests = map[string]string{}
	}
	return &cp, nil
}

// Save atomically persists cp. The stage must advance along the legal
// ordering; a backwards or equal transition without a version bump is
// rejected to protect the last safe checkpoint.
func (s *Store) Save(cp *Checkpoint) error {
	if err := cp.validate(); err != nil {
		return err
	}
	if s.Exists() {
		prev, err := s.Load()
		if err != nil {
			return err
		}
		if ordering[cp.Stage] < ordering[prev.Stage] {
			return fmt.Errorf("checkpoint: refusing to move stage %s -> %s", prev.Stage, cp.Stage)
		}
		cp.Version = prev.Version + 1
	} else if cp.Version == 0 {
		cp.Version = 1
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.path, append(data, '\n'))
}

func (cp *Checkpoint) validate() error {
	if _, ok := ordering[cp.Stage]; !ok {
		return fmt.Errorf("checkpoint: unknown stage %q", cp.Stage)
	}
	if cp.InputDigests == nil {
		cp.InputDigests = map[string]string{}
	}
	return nil
}

// VerifyInput checks a resumed input against its recorded digest. It returns
// false (never an error) when the path is unknown, so the caller recomputes
// anything it cannot prove reusable.
func (cp *Checkpoint) VerifyInput(path, digest string) bool {
	expected, ok := cp.InputDigests[path]
	return ok && expected == digest
}

// IsFragmentCompleted reports whether a fragment finished the recorded stage.
func (cp *Checkpoint) IsFragmentCompleted(path string) bool {
	for _, done := range cp.CompletedFragments {
		if done == path {
			return true
		}
	}
	return false
}

// Reached reports whether the checkpoint has reached at least the given stage.
func (cp *Checkpoint) Reached(stage Stage) bool {
	return ordering[cp.Stage] >= ordering[stage]
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".checkpoint-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
