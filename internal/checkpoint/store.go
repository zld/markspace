// Package checkpoint persists processing state so an interrupted run can be
// resumed, and refuses to resume when the inputs or configuration changed.
package checkpoint

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Stage names the processing phases in completion order.
type Stage string

const (
	StageIngest   Stage = "ingest"
	StageTimeline Stage = "timeline"
	StageRepair   Stage = "repair"
	StageFrames   Stage = "frames"
	StageVerify   Stage = "verify"
)

var stageOrder = []Stage{StageIngest, StageTimeline, StageRepair, StageFrames, StageVerify}

// Rank returns the position of a stage in the completion order, or -1.
func Rank(s Stage) int {
	for i, v := range stageOrder {
		if v == s {
			return i
		}
	}
	return -1
}

// State is the persisted checkpoint.
type State struct {
	Version      int       `json:"version"`
	InputDigest  string    `json:"input_digest"`
	ConfigDigest string    `json:"config_digest"`
	LastStage    Stage     `json:"last_stage"`
	Completed    []Stage   `json:"completed"`
	ResumeAt     int64     `json:"resume_at"`
	Fragments    int       `json:"fragments"`
	Frames       int       `json:"frames"`
	SavedAt      time.Time `json:"saved_at"`
	// Payload carries the minimal stage output needed to continue without
	// re-deriving it.
	Payload map[string]json.RawMessage `json:"payload,omitempty"`
}

// Store writes checkpoints for one path.
type Store struct {
	Path string
}

// CurrentVersion is the on-disk schema version.
const CurrentVersion = 1

var (
	ErrMismatch = errors.New("checkpoint: input or configuration changed")
	ErrCorrupt  = errors.New("checkpoint: file is corrupt")
)

// Save writes the checkpoint via a temporary file and an atomic rename in the
// same directory. A partially written checkpoint is never visible.
func (s *Store) Save(st State) error {
	if st.Version == 0 {
		st.Version = CurrentVersion
	}
	if st.SavedAt.IsZero() {
		st.SavedAt = time.Now().UTC()
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("checkpoint: marshal: %w", err)
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("checkpoint: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".checkpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("checkpoint: temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("checkpoint: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("checkpoint: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("checkpoint: close: %w", err)
	}
	if err := os.Rename(tmpName, s.Path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("checkpoint: rename: %w", err)
	}
	return nil
}

// Load reads a checkpoint and validates its schema version.
func (s *Store) Load() (State, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if st.Version != CurrentVersion {
		return st, fmt.Errorf("%w: version %d", ErrCorrupt, st.Version)
	}
	if Rank(st.LastStage) < 0 {
		return st, fmt.Errorf("%w: unknown stage %q", ErrCorrupt, st.LastStage)
	}
	return st, nil
}

// Exists reports whether a checkpoint file is present.
func (s *Store) Exists() bool {
	_, err := os.Stat(s.Path)
	return err == nil
}

// Remove deletes the checkpoint after a fully verified run.
func (s *Store) Remove() error {
	err := os.Remove(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// DigestInputs hashes the exact input bytes a checkpoint was computed from.
func DigestInputs(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		var lenBuf [8]byte
		for i := 0; i < 8; i++ {
			lenBuf[i] = byte(uint64(len(p)) >> (8 * (7 - i)))
		}
		h.Write(lenBuf[:])
		h.Write(p)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// DigestConfig hashes the run configuration that affects results.
func DigestConfig(cfg any) string {
	data, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}
