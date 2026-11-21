// Package safeio confines all file operations to an explicitly allowed root
// and provides atomic, temp-then-rename writes so a crashed process never
// leaves a half-written playlist or index.
package safeio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Root is a directory inside which all paths must resolve.
type Root struct {
	abs string
}

// NewRoot resolves root and rejects anything that is not a directory.
func NewRoot(root string) (*Root, error) {
	abs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("root %q: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root %q is not a directory", abs)
	}
	return &Root{abs: abs}, nil
}

// EnsureRoot creates root (and parents) when missing, then returns a Root.
func EnsureRoot(root string) (*Root, error) {
	abs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create root %q: %w", abs, err)
	}
	return NewRoot(abs)
}

// String returns the absolute root path.
func (r *Root) String() string { return r.abs }

// Join resolves rel against the root and rejects traversal or absolute input.
func (r *Root) Join(rel ...string) (string, error) {
	joined := filepath.Join(rel...)
	if filepath.IsAbs(joined) {
		return "", fmt.Errorf("safeio: absolute path %q not allowed", joined)
	}
	clean := filepath.Clean(filepath.Join(r.abs, joined))
	relToRoot, err := filepath.Rel(r.abs, clean)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("safeio: path %q escapes root", filepath.Join(rel...))
	}
	return clean, nil
}

// MustJoin panics on an invalid path; use only for internally built names.
func (r *Root) MustJoin(rel ...string) string {
	p, err := r.Join(rel...)
	if err != nil {
		panic(err)
	}
	return p
}

// MkdirAll creates a confined subdirectory.
func (r *Root) MkdirAll(rel string) error {
	p, err := r.Join(rel)
	if err != nil {
		return err
	}
	return os.MkdirAll(p, 0o755)
}

// ReadFile opens a confined file.
func (r *Root) ReadFile(rel string) ([]byte, error) {
	p, err := r.Join(rel)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// Open opens a confined file for reading.
func (r *Root) Open(rel string) (*os.File, error) {
	p, err := r.Join(rel)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// AtomicWrite writes data to a temp file in the destination directory and
// renames it into place, so readers never observe a partial file.
func (r *Root) AtomicWrite(rel string, data []byte) error {
	dst, err := r.Join(rel)
	if err != nil {
		return err
	}
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("atomic rename: %w", err)
	}
	return nil
}

// ListFiles returns the regular files beneath a confined subdirectory.
func (r *Root) ListFiles(rel string) ([]string, error) {
	base, err := r.Join(rel)
	if err != nil {
		return nil, err
	}
	var out []string
	err = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			relPath, relErr := filepath.Rel(base, path)
			if relErr != nil {
				return relErr
			}
			out = append(out, relPath)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return out, err
}
