// Package recovery persists pipeline progress so a retried node run resumes
// from the first unfinished stage and yields the same result as a full run.
package recovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediad/internal/core"
)

// Version is the checkpoint schema version. Loading any other version fails
// loudly rather than silently misreading fields.
const Version = 1

// MaxCheckpointBytes bounds a checkpoint file. A truncated or hostile file
// cannot drive an unbounded allocation.
const MaxCheckpointBytes = 64 << 20

// ErrDigestMismatch means the checkpoint belongs to different input bytes.
var ErrDigestMismatch = errors.New("recovery: checkpoint input digest does not match the current input")

// ErrCorruptCheckpoint means the stored stage payload no longer hashes to its
// recorded digest.
var ErrCorruptCheckpoint = errors.New("recovery: checkpoint stage payload is corrupt")

// StageRecord is one committed stage.
type StageRecord struct {
	Name           string `json:"name"`
	Digest         string `json:"digest"`
	Payload        []byte `json:"payload"`
	RecordsThrough int    `json:"records_through"` // last complete input record committed
}

// Checkpoint is the persisted journal.
type Checkpoint struct {
	Version      int           `json:"version"`
	InputDigest  string        `json:"input_digest"`
	ParamsDigest string        `json:"params_digest"`
	LastComplete string        `json:"last_complete"`
	LastRecordID int           `json:"last_record_id"`
	Stages       []StageRecord `json:"stages"`
}

// Store is the on-disk journal. A nil Path keeps the store in memory, which
// tests use to exercise resume without touching the filesystem.
type Store struct {
	Path    string
	Root    string
	cp      Checkpoint
	loaded  bool
	dirty   bool
	commits int
}

// NewStore binds a store to path. When root is non-empty the path must resolve
// inside it.
func NewStore(root, path string) (*Store, error) {
	s := &Store{Root: root, Path: path}
	if path == "" {
		s.cp = Checkpoint{Version: Version}
		return s, nil
	}
	if root != "" {
		if err := ensureWithin(root, path); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func ensureWithin(root, target string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("recovery: checkpoint path %q escapes %q", target, root)
	}
	return nil
}

// Begin initializes a fresh checkpoint bound to the input and params digest.
// If a checkpoint already exists on disk it is loaded and verified; when it
// matches, its committed stages are kept so the run can resume.
func (s *Store) Begin(inputDigest, paramsDigest string) (resumed bool, err error) {
	if s.Path != "" {
		if _, statErr := os.Stat(s.Path); statErr == nil {
			cp, err := LoadFile(s.Path)
			if err != nil {
				return false, err
			}
			if cp.InputDigest != inputDigest {
				return false, ErrDigestMismatch
			}
			if cp.ParamsDigest != paramsDigest {
				return false, fmt.Errorf("recovery: checkpoint params digest %s does not match %s", cp.ParamsDigest, paramsDigest)
			}
			s.cp = *cp
			s.loaded = true
			return len(cp.Stages) > 0, nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return false, statErr
		}
	}
	s.cp = Checkpoint{Version: Version, InputDigest: inputDigest, ParamsDigest: paramsDigest}
	s.loaded = true
	if err := s.flush(); err != nil {
		return false, err
	}
	return false, nil
}

// LoadFile reads and structurally validates a checkpoint.
func LoadFile(path string) (*Checkpoint, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxCheckpointBytes {
		return nil, fmt.Errorf("recovery: checkpoint %s exceeds %d bytes", path, MaxCheckpointBytes)
	}
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var cp Checkpoint
	if err := dec.Decode(&cp); err != nil {
		return nil, fmt.Errorf("recovery: parse checkpoint: %w", err)
	}
	if cp.Version != Version {
		return nil, fmt.Errorf("recovery: unsupported checkpoint version %d", cp.Version)
	}
	if cp.InputDigest == "" || cp.ParamsDigest == "" {
		return nil, errors.New("recovery: checkpoint is missing input or params digest")
	}
	if err := cp.Validate(); err != nil {
		return nil, err
	}
	return &cp, nil
}

// Validate checks stage ordering and payload digests.
func (cp *Checkpoint) Validate() error {
	seen := make(map[string]bool, len(cp.Stages))
	for i, st := range cp.Stages {
		if st.Name == "" {
			return fmt.Errorf("recovery: stage %d has no name", i)
		}
		if seen[st.Name] {
			return fmt.Errorf("recovery: duplicate stage %q", st.Name)
		}
		seen[st.Name] = true
		if got := core.HashParts(st.Payload); got != st.Digest {
			return fmt.Errorf("%w: stage %q expected %s got %s", ErrCorruptCheckpoint, st.Name, st.Digest, got)
		}
		if st.RecordsThrough < 0 {
			return fmt.Errorf("recovery: stage %q has negative record cursor", st.Name)
		}
	}
	return nil
}

// Lookup returns a committed stage payload after verifying its digest.
func (s *Store) Lookup(name string) ([]byte, bool, error) {
	for _, st := range s.cp.Stages {
		if st.Name != name {
			continue
		}
		if got := core.HashParts(st.Payload); got != st.Digest {
			return nil, false, fmt.Errorf("%w: stage %q", ErrCorruptCheckpoint, name)
		}
		return st.Payload, true, nil
	}
	return nil, false, nil
}

// Commit stores a stage result and persists the journal atomically. Re-committing
// the same stage replaces the earlier record so a retried stage is idempotent.
func (s *Store) Commit(name string, payload []byte, recordsThrough int) error {
	if name == "" {
		return errors.New("recovery: empty stage name")
	}
	rec := StageRecord{Name: name, Digest: core.HashParts(payload), Payload: payload, RecordsThrough: recordsThrough}
	replaced := false
	for i := range s.cp.Stages {
		if s.cp.Stages[i].Name == name {
			s.cp.Stages[i] = rec
			replaced = true
			break
		}
	}
	if !replaced {
		s.cp.Stages = append(s.cp.Stages, rec)
	}
	s.cp.LastComplete = name
	s.cp.LastRecordID = recordsThrough
	s.commits++
	return s.flush()
}

// flush writes the checkpoint atomically: temp file in the same directory,
// fsync, then rename. Readers therefore never observe a partial journal.
func (s *Store) flush() error {
	if s.Path == "" {
		return nil
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(s.cp)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".checkpoint-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.Path)
}

// Snapshot returns a copy of the journal for inspection and tests.
func (s *Store) Snapshot() Checkpoint {
	out := s.cp
	out.Stages = append([]StageRecord(nil), s.cp.Stages...)
	return out
}

// Commits returns how many stage commits have happened in this process.
func (s *Store) Commits() int { return s.commits }

// CompletedStages lists committed stage names in commit order.
func (s *Store) CompletedStages() []string {
	out := make([]string, 0, len(s.cp.Stages))
	for _, st := range s.cp.Stages {
		out = append(out, st.Name)
	}
	return out
}

// SortedStages returns committed stages ordered by name, for stable display.
func (s *Store) SortedStages() []StageRecord {
	out := append([]StageRecord(nil), s.cp.Stages...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
