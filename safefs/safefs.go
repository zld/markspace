// Package safefs confines every write to a single output directory and
// rejects path traversal. Checkpoints and artifacts are only ever written
// through these helpers.
package safefs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Join resolves name inside root, rejecting absolute paths and traversal.
func Join(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("safefs: unsafe path %q", name)
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("safefs: path traversal rejected: %q", name)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	p := filepath.Join(abs, clean)
	if p != abs && !strings.HasPrefix(p, abs+string(filepath.Separator)) {
		return "", fmt.Errorf("safefs: path escapes root: %q", name)
	}
	return p, nil
}

// MkdirAll creates a directory inside root.
func MkdirAll(root, name string, perm os.FileMode) (string, error) {
	p, err := Join(root, name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(p, perm); err != nil {
		return "", err
	}
	return p, nil
}

// WriteFileAtomic writes data to root/name via a temp file + rename, so an
// interrupted process never leaves a half-written file behind.
func WriteFileAtomic(root, name string, data []byte, perm os.FileMode) (string, error) {
	p, err := Join(root, name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, p); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return p, nil
}
