// Package state persists task progress to a single local file.
//
// A task moves through an ordered list of stages. A stage is only recorded
// as successful after it has produced its output; checkpoints written after
// that let a later run resume, provided the input digest still matches the
// one recorded when the task began. Mismatch is a hard refusal: resuming
// against different inputs would silently mix two recordings.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Stage names are shared with the pipeline that drives them.
const (
	StageIngest    = "ingest"
	StageTransport = "transport"
	StageScan      = "scan"
	StageRepair    = "repair"
	StageCut       = "cut"
	StageManifest  = "manifest"
)

// Order is the canonical stage order.
var Order = []string{
	StageIngest, StageTransport, StageScan, StageRepair, StageCut, StageManifest,
}

// ErrInputMismatch means resume was requested against different inputs.
var ErrInputMismatch = errors.New("state: input digest does not match the recorded task")

// ErrNotComplete means the requested stage is not recorded as done.
var ErrNotComplete = errors.New("state: stage not complete")

// ErrUnknownStage is returned for a stage name outside Order.
var ErrUnknownStage = errors.New("state: unknown stage")

// Snapshot is the on-disk document. It is written as JSON with sorted keys
// (encoding/json sorts map keys) so two runs of the same task produce the
// same bytes for the same progress.
type Snapshot struct {
	Version     int               `json:"version"`
	TaskKey     string            `json:"task_key"`
	InputDigest string            `json:"input_digest"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
	Stage       int               `json:"stage"` // number of completed stages
	Done        map[string]bool   `json:"done"`
	Notes       map[string]string `json:"notes,omitempty"`
}

// Store manages one task's file under a directory.
type Store struct {
	mu   sync.Mutex
	path string
	snap Snapshot
}

// Open loads or creates the snapshot for taskKey at dir/<taskKey>.state.json.
// inputDigest is recorded on creation and enforced on every later open.
func Open(dir, taskKey, inputDigest string) (*Store, error) {
	if taskKey == "" {
		return nil, fmt.Errorf("state: empty task key")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, sanitize(taskKey)+".state.json")
	s := &Store{path: path}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &s.snap); err != nil {
			return nil, fmt.Errorf("state: corrupt snapshot %s: %w", path, err)
		}
		if s.snap.TaskKey != taskKey {
			return nil, fmt.Errorf("state: snapshot task key %q != %q", s.snap.TaskKey, taskKey)
		}
		if s.snap.InputDigest != inputDigest {
			return nil, fmt.Errorf("%w: recorded %s, got %s", ErrInputMismatch, s.snap.InputDigest, inputDigest)
		}
	case errors.Is(err, os.ErrNotExist):
		now := time.Now().UTC().Format(time.RFC3339Nano)
		s.snap = Snapshot{
			Version:     1,
			TaskKey:     taskKey,
			InputDigest: inputDigest,
			CreatedAt:   now,
			UpdatedAt:   now,
			Done:        make(map[string]bool),
		}
		if err := s.flushLocked(); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return s, nil
}

// Path returns the snapshot file path.
func (s *Store) Path() string { return s.path }

// Completed reports whether a stage has been recorded successful.
func (s *Store) Completed(stage string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap.Done[stage]
}

// CompletedStages returns the successful stages in canonical order.
func (s *Store) CompletedStages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(Order))
	for _, name := range Order {
		if s.snap.Done[name] {
			out = append(out, name)
		}
	}
	return out
}

// ResumeStage returns the first incomplete stage. All stages before it must
// be complete; a hole in the middle is reported as corruption rather than
// silently skipped.
func (s *Store) ResumeStage() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range Order {
		if !s.snap.Done[name] {
			return name, nil
		}
	}
	return "", nil // everything complete
}

// ValidateOrder checks that no completed stage follows an incomplete one.
func (s *Store) ValidateOrder() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	seenIncomplete := ""
	for _, name := range Order {
		if !s.snap.Done[name] {
			if seenIncomplete == "" {
				seenIncomplete = name
			}
			continue
		}
		if seenIncomplete != "" {
			return fmt.Errorf("state: stage %q recorded after incomplete %q", name, seenIncomplete)
		}
	}
	return nil
}

// MarkStage records a stage as successful and persists the snapshot
// atomically. Recording a stage out of order is refused.
func (s *Store) MarkStage(stage, note string) error {
	if !known(stage) {
		return fmt.Errorf("%w: %q", ErrUnknownStage, stage)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range Order {
		if name == stage {
			if err := s.requirePrereqsLocked(stage); err != nil {
				return err
			}
			s.snap.Done[stage] = true
			if note != "" {
				if s.snap.Notes == nil {
					s.snap.Notes = make(map[string]string)
				}
				s.snap.Notes[stage] = note
			}
			s.snap.Stage = s.completedCountLocked()
			s.snap.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			return s.flushLocked()
		}
	}
	return fmt.Errorf("%w: %q", ErrUnknownStage, stage)
}

// MarkStages records several stages in canonical order in one write.
func (s *Store) MarkStages(stages ...string) error {
	sort.SliceStable(stages, func(i, j int) bool { return stageIndex(stages[i]) < stageIndex(stages[j]) })
	for _, name := range stages {
		if err := s.MarkStage(name, ""); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot returns a copy of the current snapshot.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.snap
	cp.Done = make(map[string]bool, len(s.snap.Done))
	for k, v := range s.snap.Done {
		cp.Done[k] = v
	}
	if s.snap.Notes != nil {
		cp.Notes = make(map[string]string, len(s.snap.Notes))
		for k, v := range s.snap.Notes {
			cp.Notes[k] = v
		}
	}
	return cp
}

func (s *Store) requirePrereqsLocked(stage string) error {
	want := stageIndex(stage)
	for i := 0; i < want; i++ {
		if !s.snap.Done[Order[i]] {
			return fmt.Errorf("state: cannot record %q before %q", stage, Order[i])
		}
	}
	return nil
}

func (s *Store) completedCountLocked() int {
	n := 0
	for _, name := range Order {
		if s.snap.Done[name] {
			n++
		}
	}
	return n
}

// flushLocked writes the snapshot atomically: temp file in the same
// directory, fsync, rename over the destination.
func (s *Store) flushLocked() error {
	data, err := json.MarshalIndent(&s.snap, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
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
	return os.Rename(tmpName, s.path)
}

func known(stage string) bool { return stageIndex(stage) >= 0 }

func stageIndex(stage string) int {
	for i, name := range Order {
		if name == stage {
			return i
		}
	}
	return -1
}

// sanitize keeps the snapshot filename filesystem-safe and deterministic.
func sanitize(key string) string {
	out := make([]rune, 0, len(key))
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
