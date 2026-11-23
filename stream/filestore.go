package stream

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// FileStore persists checkpoints as a JSON file so an interrupted process can
// resume from the last complete frame after restart.
type FileStore struct {
	path string
}

// NewFileStore opens (or prepares) a checkpoint file location.
func NewFileStore(path string) (*FileStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &FileStore{path: path}, nil
}

func (f *FileStore) Load() (Checkpoint, bool, error) {
	raw, err := os.ReadFile(f.path)
	if os.IsNotExist(err) {
		return Checkpoint{}, false, nil
	}
	if err != nil {
		return Checkpoint{}, false, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return Checkpoint{}, false, err
	}
	return cp, true, nil
}

func (f *FileStore) Save(cp Checkpoint) error {
	raw, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}
