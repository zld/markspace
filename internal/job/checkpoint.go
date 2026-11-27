package job

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"mediad/internal/clock"
)

// Store persists checkpoints and job state under a local directory. All
// writes are atomic: data lands in a temp file and is renamed into place.
type Store struct {
	Dir string
}

// NewStore creates (if needed) the store directory.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

func (s *Store) checkpointPath(p Phase) string {
	return filepath.Join(s.Dir, "checkpoint-"+string(p)+".bin")
}

func (s *Store) statePath() string {
	return filepath.Join(s.Dir, "job-state.json")
}

// atomicWrite writes data via temp file + rename.
func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// WriteCheckpoint atomically persists a phase checkpoint.
func (s *Store) WriteCheckpoint(p Phase, data []byte) error {
	return atomicWrite(s.checkpointPath(p), data)
}

// ReadCheckpoint loads a phase checkpoint.
func (s *Store) ReadCheckpoint(p Phase) ([]byte, error) {
	data, err := os.ReadFile(s.checkpointPath(p))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, clock.New(clock.ErrCheckpoint, "checkpoint %s missing", p)
		}
		return nil, err
	}
	return data, nil
}

// SaveState atomically persists the job state.
func (s *Store) SaveState(st *State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.statePath(), data)
}

// LoadState loads the persisted state, or returns a fresh one. A state file
// that does not parse is treated as corrupt and reported, not ignored.
func (s *Store) LoadState(jobID string) (*State, error) {
	data, err := os.ReadFile(s.statePath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return NewState(jobID), nil
		}
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, clock.New(clock.ErrCheckpoint, "state file corrupt: %v", err)
	}
	if st.JobID != jobID {
		// Different job in the same store: start over rather than mix.
		return NewState(jobID), nil
	}
	if st.Hashes == nil {
		st.Hashes = make(map[Phase]string)
	}
	return &st, nil
}
