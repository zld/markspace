package release

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrVerification = errors.New("release verification failed")

// LoadState reads the atomic release pointer state.
func LoadState(root string) (*State, error) {
	b, err := os.ReadFile(filepath.Join(root, StateName))
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// LoadManifest reads a version manifest from a package directory.
func LoadManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVerification, err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%w: manifest: %v", ErrVerification, err)
	}
	return &m, nil
}

// VerifyDir recomputes every artifact hash.
func VerifyDir(dir string) (*Manifest, error) {
	m, err := LoadManifest(dir)
	if err != nil {
		return nil, err
	}
	for _, a := range m.Artifacts {
		got, _, err := hashFile(filepath.Join(dir, a.Path))
		if err != nil {
			return m, fmt.Errorf("%w: %s: %v", ErrVerification, a.Path, err)
		}
		if got != a.SHA256 {
			return m, fmt.Errorf("%w: hash mismatch for %s", ErrVerification, a.Path)
		}
	}
	return m, nil
}
