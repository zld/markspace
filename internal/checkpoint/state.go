// Package checkpoint persists pipeline progress. The checkpoint advances
// only after a segment's digest has been durably written, so an
// interrupted run never re-commits confirmed segments.
package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"mediqc/internal/fault"
)

// State is the resumable progress record.
type State struct {
	Stage     string   `json:"stage"`
	Next      int      `json:"next"`      // next segment index to process
	Committed []string `json:"committed"` // segment digests in commit order
}

// Store keeps the state file and the segment digest files in a directory.
type Store struct {
	dir string
}

// Open creates (or reuses) a checkpoint directory.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "segments"), 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) statePath() string { return filepath.Join(s.dir, "state.json") }

// Load reads the state; a missing file yields a fresh state. Every
// committed digest file must still exist, otherwise the checkpoint is
// stale and the run must restart from the last consistent point.
func (s *Store) Load() (*State, error) {
	raw, err := os.ReadFile(s.statePath())
	if errors.Is(err, fs.ErrNotExist) {
		return &State{Stage: "init"}, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fault.New(fault.CodeCheckpointStale, 0, -1,
			"unreadable checkpoint: "+err.Error(), "delete the checkpoint and rerun")
	}
	if st.Next != len(st.Committed) {
		return nil, fault.New(fault.CodeCheckpointStale, 0, -1,
			fmt.Sprintf("next=%d but %d digests committed", st.Next, len(st.Committed)),
			"delete the checkpoint and rerun")
	}
	for _, d := range st.Committed {
		if _, err := os.Stat(s.SegmentPath(d)); err != nil {
			return nil, fault.New(fault.CodeCheckpointStale, 0, -1,
				"committed segment digest missing: "+d,
				"segment store lost; rerun from scratch")
		}
	}
	return &st, nil
}

// SegmentPath is where a committed segment record lives.
func (s *Store) SegmentPath(digest string) string {
	return filepath.Join(s.dir, "segments", digest+".seg")
}

// Commit writes the segment record first, then advances the checkpoint
// atomically (temp file + rename). A crash between the two leaves an
// uncommitted record that the next run simply recomputes.
func (s *Store) Commit(st *State, digest string, record []byte) error {
	seg := s.SegmentPath(digest)
	if err := os.WriteFile(seg, record, 0o644); err != nil {
		return err
	}
	st.Committed = append(st.Committed, digest)
	st.Next = len(st.Committed)
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.statePath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.statePath())
}

// SetStage updates the stage label without committing a segment.
func (s *Store) SetStage(st *State, stage string) error {
	st.Stage = stage
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.statePath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.statePath())
}
