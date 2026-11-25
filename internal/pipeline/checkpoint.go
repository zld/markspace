package pipeline

import (
	"encoding/gob"
	"encoding/json"
	"os"
	"path/filepath"
)

// SaveCheckpoint atomically persists resumable state.
func SaveCheckpoint(path string, st *State) error {
	if path == "" || st == nil {
		return nil
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	encErr := gob.NewEncoder(f).Encode(st)
	closeErr := f.Close()
	if encErr != nil {
		return encErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}

// LoadCheckpoint reads a state previously written by SaveCheckpoint.
func LoadCheckpoint(path string) (*State, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var st State
	if err := gob.NewDecoder(f).Decode(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}
