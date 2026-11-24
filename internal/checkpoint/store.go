// Package checkpoint persists pipeline progress so an interrupted run resumes
// from the most recent complete node instead of starting over.
//
// A Store is a small JSON document written atomically (temp file + rename),
// holding per-node state (status plus input/config digests), the completed
// timeline intervals and a run version. Reads and writes are fully
// deterministic, which is what makes repeated runs produce identical output
// checksums.
package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediapipeline/internal/dag"
)

// Interval mirrors clock.Interval for JSON persistence (no import cycle).
type Interval struct {
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
}

// Document is the on-disk checkpoint layout.
type Document struct {
	// Version is bumped when the layout changes.
	Version int `json:"version"`
	// RunID groups nodes of one logical pipeline run.
	RunID string `json:"run_id"`
	// Nodes maps node name to its persisted state.
	Nodes map[string]dag.State `json:"nodes"`
	// Completed holds finished timeline intervals per media kind ("audio"/"video").
	Completed map[string][]Interval `json:"completed"`
}

// CurrentVersion is the supported document layout version.
const CurrentVersion = 1

// Store implements dag.StateStore over an atomic JSON file.
type Store struct {
	path string
	doc  Document
}

// ErrNotFound is returned when no checkpoint exists at the given path.
var ErrNotFound = errors.New("checkpoint: no stored document")

// Open loads a checkpoint, creating an empty in-memory one when absent.
func Open(path, runID string) (*Store, error) {
	s := &Store{path: path, doc: Document{
		Version:   CurrentVersion,
		RunID:     runID,
		Nodes:     map[string]dag.State{},
		Completed: map[string][]Interval{},
	}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("checkpoint: read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &s.doc); err != nil {
		return nil, fmt.Errorf("checkpoint: corrupt document: %w", err)
	}
	if s.doc.Version != CurrentVersion {
		return nil, fmt.Errorf("checkpoint: unsupported version %d", s.doc.Version)
	}
	if s.doc.RunID != runID {
		return nil, fmt.Errorf("checkpoint: run id mismatch %q != %q", s.doc.RunID, runID)
	}
	if s.doc.Nodes == nil {
		s.doc.Nodes = map[string]dag.State{}
	}
	if s.doc.Completed == nil {
		s.doc.Completed = map[string][]Interval{}
	}
	return s, nil
}

// Get returns a node's persisted state (implements dag.StateStore).
func (s *Store) Get(node string) (dag.State, bool) {
	st, ok := s.doc.Nodes[node]
	return st, ok
}

// Put upserts a node state in memory; call Save to persist.
func (s *Store) Put(node string, st dag.State) error {
	if node == "" {
		return errors.New("checkpoint: node name is empty")
	}
	s.doc.Nodes[node] = st
	return nil
}

// AppendInterval records a newly completed interval for a media kind.
func (s *Store) AppendInterval(kind string, iv Interval) {
	s.doc.Completed[kind] = append(s.doc.Completed[kind], iv)
}

// Intervals returns the completed intervals for a media kind, sorted by start.
func (s *Store) Intervals(kind string) []Interval {
	out := append([]Interval(nil), s.doc.Completed[kind]...)
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// Snapshot returns a deep copy of the document for inspection/provenance.
func (s *Store) Snapshot() Document {
	d := Document{Version: s.doc.Version, RunID: s.doc.RunID,
		Nodes: map[string]dag.State{}, Completed: map[string][]Interval{}}
	for k, v := range s.doc.Nodes {
		d.Nodes[k] = v
	}
	for k, v := range s.doc.Completed {
		d.Completed[k] = append([]Interval(nil), v...)
	}
	return d
}

// Save atomically writes the document (temp file in the same dir + rename).
func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("checkpoint: mkdir: %w", err)
	}
	raw, err := json.MarshalIndent(&s.doc, "", "  ")
	if err != nil {
		return fmt.Errorf("checkpoint: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("checkpoint: write temp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("checkpoint: rename: %w", err)
	}
	return nil
}

// Path returns the checkpoint file location.
func (s *Store) Path() string { return s.path }
