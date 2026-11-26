// Package index stores content digests and enforces product state transitions.
package index

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mediapipeline/internal/evidence"
)

// State is the product index state. Only pending -> published is allowed.
type State string

const (
	StatePending   State = "pending"
	StatePublished State = "published"
)

var (
	ErrInvalidTransition = errors.New("index: invalid state transition")
	ErrAlreadyPublished  = errors.New("index: product already published")
	ErrNotFound          = errors.New("index: product not found")
)

// Artifact is the single record retained for one deterministic product key.
type Artifact struct {
	Key          string    `json:"key"`
	State        State     `json:"state"`
	ProductPath  string    `json:"product_path,omitempty"`
	TempPath     string    `json:"temp_path,omitempty"`
	SourcePath   string    `json:"source_path,omitempty"`
	SourceSHA256 string    `json:"source_sha256,omitempty"`
	TrackSHA256  string    `json:"track_sha256,omitempty"`
	OutputSHA256 string    `json:"output_sha256,omitempty"`
	Size         int64     `json:"size,omitempty"`
	JournalID    string    `json:"journal_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	PublishedAt  time.Time `json:"published_at,omitempty"`
	RecoveredAt  time.Time `json:"recovered_at,omitempty"`
}

// Store is a JSON file index with in-process keyed mutual exclusion.
type Store struct {
	path     string
	mu       sync.Mutex
	keyLocks map[string]*sync.Mutex
	Records  map[string]*Artifact `json:"records"`
}

// Open loads or initializes an index file.
func Open(path string) (*Store, error) {
	s := &Store{path: path, keyLocks: make(map[string]*sync.Mutex), Records: make(map[string]*Artifact)}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("index: parse %s: %w", path, err)
	}
	if s.Records == nil {
		s.Records = make(map[string]*Artifact)
	}
	if s.keyLocks == nil {
		s.keyLocks = make(map[string]*sync.Mutex)
	}
	return s, nil
}

// LockKey returns a process-wide mutex for one deterministic product key.
func (s *Store) LockKey(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.keyLocks[key]
	if !ok {
		m = &sync.Mutex{}
		s.keyLocks[key] = m
	}
	return m
}

// Get returns a copy? The pointer is intentionally managed under key lock.
func (s *Store) Get(key string) (*Artifact, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.Records[key]
	return a, ok
}

// EnsurePending creates the unique pending record or returns the existing one.
func (s *Store) EnsurePending(a Artifact) (*Artifact, bool, error) {
	if a.Key == "" {
		return nil, false, errors.New("index: product key is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.Records[a.Key]; ok {
		return old, false, nil
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	a.State = StatePending
	rec := a
	s.Records[a.Key] = &rec
	if err := s.saveLocked(); err != nil {
		return nil, false, err
	}
	return &rec, true, nil
}

// MarkPublished performs the only legal transition, pending -> published.
func (s *Store) MarkPublished(key, productPath, outputSHA string, size int64) (*Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.Records[key]
	if !ok {
		return nil, ErrNotFound
	}
	switch a.State {
	case StatePublished:
		if a.OutputSHA256 == outputSHA && a.ProductPath == productPath {
			return a, fmt.Errorf("%w: %s", ErrAlreadyPublished, key)
		}
		return nil, fmt.Errorf("%w: published key %s with different artifact", ErrInvalidTransition, key)
	case StatePending:
	default:
		return nil, fmt.Errorf("%w: %s state=%s", ErrInvalidTransition, key, a.State)
	}
	a.State = StatePublished
	a.ProductPath = productPath
	a.OutputSHA256 = outputSHA
	a.Size = size
	a.PublishedAt = time.Now().UTC()
	a.RecoveredAt = time.Time{}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return a, nil
}

// MarkRecovered marks a pending record as published after journal verification.
func (s *Store) MarkRecovered(key, productPath, outputSHA string, size int64) (*Artifact, error) {
	a, err := s.MarkPublished(key, productPath, outputSHA, size)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	a.RecoveredAt = time.Now().UTC()
	err = s.saveLocked()
	s.mu.Unlock()
	return a, err
}

// ReplacePublished repairs one already-published key after its corrupt file was
// quarantined. The state remains published and the key still has one artifact.
func (s *Store) ReplacePublished(key, productPath, outputSHA string, size int64) (*Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.Records[key]
	if !ok {
		return nil, ErrNotFound
	}
	if a.State != StatePublished {
		return nil, fmt.Errorf("%w: repair requires published state, key=%s state=%s", ErrInvalidTransition, key, a.State)
	}
	a.ProductPath = productPath
	a.OutputSHA256 = outputSHA
	a.Size = size
	a.PublishedAt = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return a, nil
}

// saveLocked atomically persists the JSON index. Callers must hold s.mu.
func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_RDWR, 0o644)
	if err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(s.path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// ProductKey computes a stable content/version product key.
func ProductKey(parts ...string) string {
	byteParts := make([][]byte, 0, len(parts)+1)
	byteParts = append(byteParts, []byte("mediapipeline-product-v1"))
	for _, p := range parts {
		byteParts = append(byteParts, []byte(p))
	}
	sum := evidence.HashParts(byteParts...)
	b, _ := hex.DecodeString(sum)
	return hex.EncodeToString(b)
}
