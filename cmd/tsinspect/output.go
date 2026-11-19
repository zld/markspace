package main

import (
	"os"
	"path/filepath"
)

// atomicFile writes to a temporary file in the destination directory and
// renames it into place only on success, preserving any previous file.
type atomicFile struct {
	path string
	tmp  *os.File
	done bool
}

func createAtomic(path string) (*atomicFile, error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	// CreateTemp permutes the name, so an abandoned temporary never shadows
	// a real output file.
	f, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return nil, err
	}
	return &atomicFile{path: path, tmp: f}, nil
}

func (a *atomicFile) Write(p []byte) (int, error) { return a.tmp.Write(p) }

// Finish commits or discards the temporary file. Discard removes only the
// temporary path; the previous destination stays untouched.
func (a *atomicFile) Finish(commit bool) error {
	if a.done {
		return nil
	}
	a.done = true
	name := a.tmp.Name()
	if commit {
		// Flush file contents before the rename so a crash cannot leave a
		// renamed-but-empty slice.
		if err := a.tmp.Sync(); err != nil {
			a.tmp.Close()
			os.Remove(name)
			return err
		}
	}
	if err := a.tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if !commit {
		os.Remove(name)
		return nil
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, a.path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
