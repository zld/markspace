// Package store provides atomic file publication: data is written to a
// temporary file and renamed into place, so readers never see partial
// output; stale temp files from interrupted runs are cleaned up.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const tmpSuffix = ".tmp"

// AtomicFile writes to a temp file until Close renames it into place.
type AtomicFile struct {
	f     *os.File
	tmp   string
	final string
	done  bool
}

// Create makes an atomic file to be published as dir/name.
func Create(dir, name string) (*AtomicFile, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	tmp := filepath.Join(dir, name+tmpSuffix)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	return &AtomicFile{f: f, tmp: tmp, final: filepath.Join(dir, name)}, nil
}

// Write appends to the temp file.
func (a *AtomicFile) Write(p []byte) (int, error) { return a.f.Write(p) }

// Close syncs and atomically renames the temp file to its final name.
func (a *AtomicFile) Close() error {
	if a.done {
		return nil
	}
	a.done = true
	if err := a.f.Sync(); err != nil {
		a.f.Close()
		os.Remove(a.tmp)
		return err
	}
	if err := a.f.Close(); err != nil {
		os.Remove(a.tmp)
		return err
	}
	return os.Rename(a.tmp, a.final)
}

// Abort discards the temp file without publishing.
func (a *AtomicFile) Abort() {
	if a.done {
		return
	}
	a.done = true
	a.f.Close()
	os.Remove(a.tmp)
}

// CleanTemps removes leftover temp files in dir from interrupted runs.
func CleanTemps(dir string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), tmpSuffix) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return fmt.Errorf("clean temp: %w", err)
			}
		}
	}
	return nil
}
