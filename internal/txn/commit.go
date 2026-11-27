package txn

import (
	"os"
	"path/filepath"

	perr "gamepack/internal/errors"
)

// tempPrefix marks in-directory temporary files so crash leftovers are
// recognizable and cleanable.
const tempPrefix = ".packtmp-"

// AtomicFile stages content in a same-directory temporary file, fsyncs it and
// then atomically renames it over the target. A crash at any point leaves
// either the old file or a recognizable .packtmp-* leftover, never a partial
// target.
type AtomicFile struct {
	dir    string
	target string
	tmp    *os.File
	closed bool
}

// CreateAtomic opens a temporary file in target's own directory (required for
// an atomic rename) with owner-only permissions.
func CreateAtomic(target string) (*AtomicFile, error) {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, perr.New(subsystem, perr.CodeTxn, "mkdir: %v", err)
	}
	tmp, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return nil, perr.New(subsystem, perr.CodeTxn, "create temp: %v", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, perr.New(subsystem, perr.CodeTxn, "chmod temp: %v", err)
	}
	return &AtomicFile{dir: dir, target: target, tmp: tmp}, nil
}

// Write appends content to the staging file.
func (a *AtomicFile) Write(p []byte) (int, error) {
	return a.tmp.Write(p)
}

// Commit fsyncs the temp file, closes it and renames it onto the target.
func (a *AtomicFile) Commit() error {
	if a.closed {
		return perr.New(subsystem, perr.CodeTxn, "atomic file already finalized")
	}
	if err := a.tmp.Sync(); err != nil {
		return a.fail("fsync temp: %v", err)
	}
	name := a.tmp.Name()
	if err := a.tmp.Close(); err != nil {
		return a.fail("close temp: %v", err)
	}
	a.closed = true
	if err := os.Rename(name, a.target); err != nil {
		return perr.New(subsystem, perr.CodeTxn, "atomic rename: %v", err)
	}
	return syncDir(a.dir)
}

// Abort closes and removes the staging file, leaving the target untouched.
func (a *AtomicFile) Abort() {
	if a.closed {
		return
	}
	name := a.tmp.Name()
	_ = a.tmp.Close()
	_ = os.Remove(name)
	a.closed = true
}

func (a *AtomicFile) fail(format string, err error) error {
	a.Abort()
	return perr.New(subsystem, perr.CodeTxn, format, err)
}

// WriteAtomic is a one-shot helper: stage, write and commit content.
func WriteAtomic(target string, content []byte) error {
	a, err := CreateAtomic(target)
	if err != nil {
		return err
	}
	if _, err := a.Write(content); err != nil {
		a.Abort()
		return perr.New(subsystem, perr.CodeTxn, "write temp: %v", err)
	}
	return a.Commit()
}

// CleanLeftovers removes crash-residue temporary files from dir and returns
// the number cleaned. It only touches files carrying the .packtmp- marker.
func CleanLeftovers(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, perr.New(subsystem, perr.CodeTxn, "read dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) >= len(tempPrefix) && name[:len(tempPrefix)] == tempPrefix {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return n, perr.New(subsystem, perr.CodeTxn,
					"remove leftover %s: %v", name, err)
			}
			n++
		}
	}
	return n, nil
}

// syncDir fsyncs a directory so a rename's directory entry is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return perr.New(subsystem, perr.CodeTxn, "open dir for fsync: %v", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return perr.New(subsystem, perr.CodeTxn, "fsync dir: %v", err)
	}
	return nil
}
