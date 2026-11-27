// Package checkpoint persists per-stage pipeline state as JSON so a
// restarted run resumes from the last fully completed stage. Writes are
// atomic (temp file + rename) and confined to the checkpoint directory.
package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store keeps one JSON file per stage inside dir.
type Store struct {
	dir string
}

// NewStore creates (if needed) a store rooted at dir.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// pathFor maps a stage name to its file, rejecting path escapes.
func (s *Store) pathFor(stage string) (string, error) {
	if stage == "" || strings.ContainsAny(stage, `/\`) || strings.Contains(stage, "..") {
		return "", fmt.Errorf("checkpoint: invalid stage name %q", stage)
	}
	return filepath.Join(s.dir, stage+".json"), nil
}

// Save writes state for stage atomically. A stage is only marked
// complete once its file is fully written and renamed into place.
func (s *Store) Save(stage string, state any) error {
	p, err := s.pathFor(stage)
	if err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

// Load reads stage state into out. It returns false if the stage has no
// completed checkpoint.
func (s *Store) Load(stage string, out any) (bool, error) {
	p, err := s.pathFor(stage)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, fmt.Errorf("checkpoint: corrupt state for stage %q: %w", stage, err)
	}
	return true, nil
}

// Completed reports whether stage has a readable checkpoint.
func (s *Store) Completed(stage string) bool {
	p, err := s.pathFor(stage)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}
