// Package commit owns atomic commit boundaries: every output is first
// written as a hidden temporary file in the SAME directory (so rename
// stays on one filesystem), hashed, recorded in the WAL, then renamed
// into place. Recovery accepts only commits whose rehashed content
// matches the WAL; half-written files can never replace a good one.
package commit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"

	"mediq/internal/journal"
)

// TempPrefix marks staging files belonging to an uncommitted stage.
const TempPrefix = ".mediq-tmp-"

// StageWriter stages one output file atomically.
type StageWriter struct {
	dir     string
	final   string
	tmp     *os.File
	tmpPath string
	hasher  hash.Hash
	closed  bool
}

// Create opens a staging file in the directory of finalPath. The
// caller must call Commit (or Abort) exactly once.
func Create(finalPath string) (*StageWriter, error) {
	dir := filepath.Dir(finalPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, TempPrefix+"*")
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	return &StageWriter{
		dir: dir, final: finalPath, tmp: tmp, tmpPath: tmp.Name(),
		hasher: h,
	}, nil
}

// Write buffers output content and feeds the hash simultaneously.
func (s *StageWriter) Write(p []byte) (int, error) {
	n, err := s.tmp.Write(p)
	if n > 0 {
		s.hasher.Write(p[:n])
	}
	return n, err
}

// Hash returns the hex SHA-256 of everything written so far.
func (s *StageWriter) Hash() string {
	return hex.EncodeToString(s.hasher.Sum(nil))
}

// TmpPath returns the staging path (for WAL bookkeeping and rollback).
func (s *StageWriter) TmpPath() string { return s.tmpPath }

// FinalPath returns the destination path.
func (s *StageWriter) FinalPath() string { return s.final }

// Commit flushes, fsyncs and atomically renames the temp file into
// place. On POSIX rename within one directory is atomic, so readers
// see either the old or the new file, never a partial one.
func (s *StageWriter) Commit(w *journal.WAL, stage string, meta map[string]string) (string, error) {
	if s.closed {
		return "", fmt.Errorf("commit: stage %s already finalized", stage)
	}
	s.closed = true
	hash := s.Hash()
	if err := s.tmp.Sync(); err != nil {
		s.cleanup()
		return "", err
	}
	if err := s.tmp.Close(); err != nil {
		s.cleanup()
		return "", err
	}
	if _, err := w.Prepared(stage, "", hash, []string{s.tmpPath}, meta); err != nil {
		s.cleanup()
		return "", err
	}
	if err := os.Rename(s.tmpPath, s.final); err != nil {
		if _, werr := w.Aborted(stage, map[string]string{"error": err.Error()}); werr != nil {
			return "", fmt.Errorf("rename: %w (wal abort: %v)", err, werr)
		}
		s.cleanup()
		return "", err
	}
	if err := syncDir(s.dir); err != nil {
		return hash, err
	}
	if _, err := w.Committed(stage, hash, []string{s.final}); err != nil {
		return hash, err
	}
	return hash, nil
}

// Abort discards the staging file and records an abort.
func (s *StageWriter) Abort(w *journal.WAL, stage string) error {
	if s.closed {
		return nil
	}
	s.closed = true
	if err := s.tmp.Close(); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(s.tmpPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	_, err := w.Aborted(stage, nil)
	return err
}

func (s *StageWriter) cleanup() {
	_ = os.Remove(s.tmpPath)
}

// HashFile returns the hex SHA-256 of a committed file.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// syncDir fsyncs a directory so a rename is durable after a power loss.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
