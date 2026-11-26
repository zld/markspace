package index

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// SnapshotStore persists the latest source digest snapshots.
type SnapshotStore struct {
	path      string
	mu        sync.Mutex
	Snapshots map[string]Snapshot `json:"snapshots"`
}

// OpenSnapshots loads or initializes a snapshot index.
func OpenSnapshots(path string) (*SnapshotStore, error) {
	s := &SnapshotStore{path: path, Snapshots: make(map[string]Snapshot)}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Snapshots == nil {
		s.Snapshots = make(map[string]Snapshot)
	}
	return s, nil
}

// Get returns a snapshot for a stable source label.
func (s *SnapshotStore) Get(label string) (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.Snapshots[label]
	return v, ok
}

// Put stores and atomically persists a snapshot.
func (s *SnapshotStore) Put(label string, snap Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Snapshots[label] = snap
	return s.saveLocked()
}

func (s *SnapshotStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(s.path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
