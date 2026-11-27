package merge

import (
	"errors"
	"os"
	"path/filepath"
)

// Stager implements the two-phase output protocol.  Stage writes a pending
// manifest under the staging directory; Commit atomically promotes it to the
// final path; Rollback removes staged bytes and records why validation failed.
//
// The last successfully committed manifest is never overwritten by a failed
// attempt: promotion is a single rename, and rollback only touches staging.
type Stager struct {
	dir      string // working directory holding staging and final files
	final    string // committed manifest path
	staged   string // pending manifest path
	reasonLog string // path receiving rollback reasons
}

// NewStager prepares a stager rooted at dir.  The directory is created on
// demand so callers can hand it a fresh temp directory.
func NewStager(dir string) (*Stager, error) {
	if dir == "" {
		return nil, errors.New("merge: empty stager directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Stager{
		dir:       dir,
		final:    filepath.Join(dir, "manifest.json"),
		staged:   filepath.Join(dir, "manifest.staging"),
		reasonLog: filepath.Join(dir, "rollback.log"),
	}, nil
}

// Stage writes the canonical manifest to the staging path, without touching the
// committed manifest.  The digest is recomputed from the canonical bytes.
func (s *Stager) Stage(m *Manifest) error {
	b, err := CanonicalBytes(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".stage-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, s.staged); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// Commit promotes the staged manifest to the final path atomically and marks
// the manifest committed.  It fails when nothing has been staged.
func (s *Stager) Commit(m *Manifest) error {
	if _, err := os.Stat(s.staged); err != nil {
		return errors.New("merge: nothing staged to commit")
	}
	if err := os.Rename(s.staged, s.final); err != nil {
		return err
	}
	m.Committed = true
	m.RolledBack = false
	m.FailReason = ""
	return nil
}

// Rollback discards the staged output, keeps the previous committed manifest
// untouched and persists the failure reason.  The manifest is marked
// rolled-back so callers can report the outcome.
func (s *Stager) Rollback(m *Manifest, reason string) error {
	if err := os.Remove(s.staged); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(s.reasonLog, []byte(reason+"\n"), 0o644); err != nil {
		return err
	}
	m.Committed = false
	m.RolledBack = true
	m.FailReason = reason
	return nil
}

// FinalPath reports where a committed manifest lives.
func (s *Stager) FinalPath() string { return s.final }

// StagedPath reports the pending manifest path.
func (s *Stager) StagedPath() string { return s.staged }

// ReasonPath reports the rollback reason log path.
func (s *Stager) ReasonPath() string { return s.reasonLog }
