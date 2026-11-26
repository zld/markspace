package journal

import (
	"encoding/json"
	"os"
	"path/filepath"

	"replayctl/internal/fault"
)

// ManifestPath returns the canonical manifest path.
func (s *Store) ManifestPath() string { return filepath.Join(s.Dir, ManifestName) }

// WriteManifest atomically writes and fsyncs the verified manifest.
func (s *Store) WriteManifest(m Manifest) error {
	path := s.ManifestPath()
	tmp, err := os.CreateTemp(s.Dir, ".manifest-*.json")
	if err != nil {
		return fault.New(fault.Journal, fault.StageJournal, path, -1, -1, "create manifest temp: %v", err)
	}
	tmpName := tmp.Name()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fault.New(fault.Journal, fault.StageJournal, path, -1, -1, "encode manifest: %v", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fault.New(fault.Journal, fault.StageJournal, path, -1, -1, "write manifest: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fault.New(fault.Journal, fault.StageJournal, path, -1, -1, "sync manifest: %v", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fault.New(fault.Journal, fault.StageJournal, path, -1, -1, "close manifest: %v", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fault.New(fault.Journal, fault.StageJournal, path, -1, -1, "rename manifest: %v", err)
	}
	if err := SyncDirectory(s.Dir); err != nil {
		return err
	}
	return nil
}

// ReadManifest loads the canonical manifest.
func ReadManifest(dir string) (*Manifest, error) {
	root, err := ValidateOutputPath(dir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, ManifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.New(fault.Journal, fault.StageJournal, path, -1, -1, "read manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fault.New(fault.Journal, fault.StageJournal, path, -1, -1, "parse manifest: %v", err)
	}
	return &m, nil
}
