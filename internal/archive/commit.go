package archive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	merr "medialib/internal/errors"
	"medialib/internal/fingerprint"
)

// Store commits verified artifacts into one directory, atomically,
// behind the journal. All reads and writes stay inside Dir.
type Store struct {
	Dir        string
	J          *Journal
	RolledBack int // uncommitted temp files removed during recovery
	registry   map[string]string
}

// OpenStore opens dir and recovers: temp files with no commit record
// are rolled back; committed-but-not-renamed temp files are rolled
// forward. Recovery resumes from the last committed boundary.
func OpenStore(dir string) (*Store, error) {
	j, err := OpenJournal(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{Dir: dir, J: j, registry: map[string]string{}}
	if data, err := os.ReadFile(filepath.Join(dir, "fingerprints.json")); err == nil {
		if err := json.Unmarshal(data, &s.registry); err != nil {
			return nil, merr.New(merr.StageRecover, -1, "registry", err)
		}
	}
	committed := map[string]bool{}
	for _, e := range j.Entries() {
		if e.Op == "commit" {
			committed[e.Artifact] = true
		}
	}
	tmps, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		return nil, merr.New(merr.StageRecover, -1, "glob", err)
	}
	for _, tmp := range tmps {
		base := strings.TrimSuffix(filepath.Base(tmp), ".tmp")
		if committed[base] {
			if err := os.Rename(tmp, filepath.Join(dir, base)); err != nil { // roll forward
				return nil, merr.New(merr.StageRecover, -1, "roll-forward", err)
			}
			continue
		}
		if err := os.Remove(tmp); err != nil { // roll back uncommitted work
			return nil, merr.New(merr.StageRecover, -1, "rollback", err)
		}
		if err := j.Append("recover", "rollback", base, ""); err != nil {
			return nil, err
		}
		s.RolledBack++
	}
	return s, nil
}

// Commit journals the stage, writes a temp artifact, verifies its
// hash, journals the commit and atomically renames it into place.
// Material whose fingerprint is already committed is reused without
// rewriting. Corrupt data (hash mismatch) never touches the archive.
func (s *Store) Commit(stage, name string, data []byte, hash string) (bool, error) {
	if strings.ContainsAny(name, "/\\") || name == "" {
		return false, merr.Newf(merr.StageCommit, -1, "name", "invalid artifact %q", name)
	}
	if got := fingerprint.HashBytes(data); got != hash {
		return false, merr.Newf(merr.StageCommit, -1, "verify",
			"hash mismatch for %s: got %s want %s", name, got, hash)
	}
	final := filepath.Join(s.Dir, name)
	if prev, ok := s.registry[hash]; ok {
		if ok, _ := s.Verify(prev, hash); ok {
			return true, nil // identical material already committed
		}
	}
	if err := s.J.Append(stage, "begin", name, hash); err != nil {
		return false, err
	}
	tmp := final + ".tmp"
	if err := writeFileSync(tmp, data); err != nil {
		return false, merr.New(merr.StageCommit, -1, "write-tmp", err)
	}
	if got := fingerprint.HashBytes(mustRead(tmp)); got != hash {
		os.Remove(tmp)
		return false, merr.Newf(merr.StageCommit, -1, "verify-tmp",
			"temp artifact corrupt: got %s", got)
	}
	if err := s.J.Append(stage, "commit", name, hash); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return false, merr.New(merr.StageCommit, -1, "rename", err)
	}
	s.registry[hash] = name
	return false, s.saveRegistry()
}

// WriteUncommitted simulates an interrupted stage: the begin record
// and temp file exist, but no commit follows.
func (s *Store) WriteUncommitted(stage, name string, data []byte) error {
	if err := s.J.Append(stage, "begin", name, fingerprint.HashBytes(data)); err != nil {
		return err
	}
	return writeFileSync(filepath.Join(s.Dir, name+".tmp"), data)
}

// Verify rehashes a committed artifact for off-line verification.
func (s *Store) Verify(name, hash string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, name))
	if err != nil {
		return false, merr.New(merr.StageVerify, -1, "read", err)
	}
	return fingerprint.HashBytes(data) == hash, nil
}

// LastCommitted returns the most recently committed artifact name.
func (s *Store) LastCommitted() string {
	for i := len(s.J.Entries()) - 1; i >= 0; i-- {
		if e := s.J.Entries()[i]; e.Op == "commit" {
			return e.Artifact
		}
	}
	return ""
}

// Close shuts the journal.
func (s *Store) Close() error { return s.J.Close() }

func (s *Store) saveRegistry() error {
	data, err := json.Marshal(s.registry)
	if err != nil {
		return merr.New(merr.StageCommit, -1, "registry", err)
	}
	return writeFileSync(filepath.Join(s.Dir, "fingerprints.json"), data)
}

func writeFileSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func mustRead(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return data
}
