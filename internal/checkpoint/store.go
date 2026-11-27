// Package checkpoint persists pipeline progress atomically. A checkpoint is
// only written after a stage's output and its digest have been durably
// stored, so recovery always resumes from a fully committed input offset.
// Writes go through a temp file and an atomic rename; stale checkpoints whose
// input or configuration digest changed are rejected rather than reused.
package checkpoint

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"example.com/continuity/internal/fault"
)

const (
	dirName  = ".checkpoint"
	fileName = "state.json"
	tmpName  = "state.tmp"
)

// State is the resumable pipeline position.
type State struct {
	// Version of the checkpoint schema.
	Version int `json:"version"`
	// InputDigest identifies the exact input bytes being processed.
	InputDigest string `json:"input_digest"`
	// ConfigDigest identifies the exact configuration in effect.
	ConfigDigest string `json:"config_digest"`
	// InputOffset is the last fully committed byte offset of the input.
	InputOffset int64 `json:"input_offset"`
	// FrameCount is the number of fully committed frames.
	FrameCount int64 `json:"frame_count"`
	// Stages records the last committed offset per processing stage.
	Stages map[string]int64 `json:"stages"`
	// Outputs lists durable artifacts already written.
	Outputs []string `json:"outputs"`
	// OutputDigest covers the manifest summary bytes produced so far, so a
	// retry cannot change already-committed output.
	OutputDigest string `json:"output_digest"`
}

// Store owns the checkpoint directory inside a run directory.
type Store struct {
	dir string
}

// NewStore opens (or creates) the checkpoint store under outDir.
func NewStore(outDir string) *Store {
	return &Store{dir: filepath.Join(outDir, dirName)}
}

func (s *Store) path() string    { return filepath.Join(s.dir, fileName) }
func (s *Store) tmpPath() string { return filepath.Join(s.dir, tmpName) }

// Exists reports whether a committed checkpoint is present.
func (s *Store) Exists() bool {
	_, err := os.Stat(s.path())
	return err == nil
}

// Save atomically persists state. The temp file is fsynced before rename and
// the directory is fsynced afterwards so the commit survives a crash.
func (s *Store) Save(state *State) error {
	if state == nil {
		return fault.New(fault.StageCheckpoint, "save", errors.New("nil state"))
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fault.New(fault.StageCheckpoint, "mkdir", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fault.New(fault.StageCheckpoint, "marshal", err)
	}
	tmp := s.tmpPath()
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fault.New(fault.StageCheckpoint, "open-temp", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fault.New(fault.StageCheckpoint, "write-temp", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fault.New(fault.StageCheckpoint, "sync-temp", err)
	}
	if err := f.Close(); err != nil {
		return fault.New(fault.StageCheckpoint, "close-temp", err)
	}
	if err := os.Rename(tmp, s.path()); err != nil {
		return fault.New(fault.StageCheckpoint, "rename", err)
	}
	return syncDir(s.dir)
}

// Load reads and validates a committed checkpoint, rejecting any whose input
// or configuration digest does not match the current run.
func (s *Store) Load(inputDigest, configDigest string) (*State, error) {
	data, err := os.ReadFile(s.path())
	if err != nil {
		return nil, fault.New(fault.StageCheckpoint, "read", err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fault.New(fault.StageCheckpoint, "parse", err)
	}
	if state.InputDigest != inputDigest || state.ConfigDigest != configDigest {
		return nil, fault.New(fault.StageCheckpoint, "load",
			fmt.Errorf("%w: input=%s config=%s", fault.ErrStaleCheckpoint,
				state.InputDigest, state.ConfigDigest))
	}
	if state.Stages == nil {
		state.Stages = map[string]int64{}
	}
	return &state, nil
}

// Reset deletes all checkpoint and temp state, used when a stale checkpoint
// must be replaced by a fresh run.
func (s *Store) Reset() error {
	if err := os.RemoveAll(s.dir); err != nil {
		return fault.New(fault.StageCheckpoint, "reset", err)
	}
	return nil
}

// CleanTemp removes any leftover temp artifacts from an interrupted run.
func (s *Store) CleanTemp() error {
	if err := os.Remove(s.tmpPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fault.New(fault.StageCheckpoint, "clean-temp", err)
	}
	return nil
}

// DigestOf returns the hex SHA-256 of data, the canonical digest helper.
func DigestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fault.New(fault.StageCheckpoint, "open-dir", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Some filesystems do not support fsync on directories; the rename
		// itself is still atomic, so treat this as non-fatal locally.
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return nil
		}
		return fault.New(fault.StageCheckpoint, "sync-dir", err)
	}
	return nil
}
