// Package checkpoint persists resumable pipeline state. Every checkpoint is
// bound to the exact input stream and index versions by SHA-256; a resume
// against changed input is refused instead of silently mixing versions.
// Writes are atomic: a temp file is fsynced and renamed over the target so
// a crash never leaves a half-written success record.
package checkpoint

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediad/internal/errors"
)

// State is the resumable record of one run.
type State struct {
	FixtureID    string   `json:"fixture_id"`
	InputHash    string   `json:"input_hash"`
	IndexHash    string   `json:"index_hash"`
	LastDoneAU   uint64   `json:"last_done_au"` // highest fully processed AU index
	CompletedAU  []uint64 `json:"completed_au"` // all committed AU indices, sorted
	SegmentCount int      `json:"segment_count"`
	ManifestHash string   `json:"manifest_hash"`
}

// Store reads and writes one checkpoint file atomically.
type Store struct{ path string }

// NewStore targets path; parent directories are created on Save.
func NewStore(path string) *Store { return &Store{path: path} }

// Path exposes the on-disk location for diagnostics.
func (s *Store) Path() string { return s.path }

// Exists reports whether a checkpoint file is present.
func (s *Store) Exists() bool {
	_, err := os.Stat(s.path)
	return err == nil
}

// Load reads and decodes the checkpoint. A missing file returns
// (nil, nil) so callers can branch on fresh start vs resume.
func (s *Store) Load() (*State, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeIOFailure, "read checkpoint: %v", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeIOFailure, "decode checkpoint: %v", err)
	}
	return &st, nil
}

// Verify refuses a resume whose input or index hash differs from the stored
// binding. A nil checkpoint means no prior run, which is always allowed.
func Verify(st *State, inputHash, indexHash string) error {
	if st == nil {
		return nil
	}
	if st.InputHash != inputHash || st.IndexHash != indexHash {
		return errs.New(errs.Fatal, errs.CodeCheckpointMismatch,
			"checkpoint bound to input=%s index=%s, got input=%s index=%s",
			st.InputHash, st.IndexHash, inputHash, indexHash)
	}
	return nil
}

// Save atomically replaces the checkpoint file.
func (s *Store) Save(st *State) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return errs.New(errs.Fatal, errs.CodeIOFailure, "checkpoint dir: %v", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return errs.New(errs.Fatal, errs.CodeInternal, "encode checkpoint: %v", err)
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return errs.New(errs.Fatal, errs.CodeIOFailure, "create temp checkpoint: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return errs.New(errs.Fatal, errs.CodeIOFailure, "write checkpoint: %v", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return errs.New(errs.Fatal, errs.CodeIOFailure, "sync checkpoint: %v", err)
	}
	if err := f.Close(); err != nil {
		return errs.New(errs.Fatal, errs.CodeIOFailure, "close checkpoint: %v", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return errs.New(errs.Fatal, errs.CodeIOFailure, "rename checkpoint: %v", err)
	}
	return nil
}

// MarkDone records a freshly committed AU and advances LastDoneAU.
func (st *State) MarkDone(au uint64) {
	st.CompletedAU = append(st.CompletedAU, au)
	if au+1 > st.LastDoneAU {
		st.LastDoneAU = au + 1
	}
}
