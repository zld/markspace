// Package cache manages parse/structure/index cache validity from three
// independent fingerprints: essence content, container structure and metadata.
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"assetcheck/internal/atomicfile"
	"assetcheck/internal/errmodel"
)

// CacheFile is the on-disk cache document name inside the index directory.
const CacheFile = "cache.json"

// Schema version.
const Schema = "assetcheck-cache/v1"

// Fingerprints are the three independent invalidation axes.
type Fingerprints struct {
	Content   string `json:"content"`
	Structure string `json:"structure"`
	Metadata  string `json:"metadata"`
}

// Entry is one cached path record.
type Entry struct {
	Path          string       `json:"path"`
	Fingerprints  Fingerprints `json:"fingerprints"`
	SummaryDigest string       `json:"summary-digest,omitempty"`
	IndexSeq      uint64       `json:"index-seq,omitempty"`
}

// Invalidation explains one invalidated cache object.
type Invalidation struct {
	Object string `json:"object"` // parse-cache | structure-cache | time-index
	Reason string `json:"reason"`
	Old    string `json:"old-fingerprint,omitempty"`
	New    string `json:"new-fingerprint,omitempty"`
}

// Doc is the persisted cache document.
type Doc struct {
	Schema  string           `json:"schema"`
	Entries map[string]Entry `json:"entries"`
}

// Store is a JSON cache store with atomic writes.
type Store struct {
	mu   sync.Mutex
	path string
	doc  Doc
}

// Open loads or initializes the cache file at path.
func Open(path string) (*Store, error) {
	s := &Store{path: path, doc: Doc{Schema: Schema, Entries: map[string]Entry{}}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, errmodel.New(errmodel.ErrCacheSchema, "read cache: %v", err)
	}
	if err := json.Unmarshal(b, &s.doc); err != nil {
		return nil, errmodel.New(errmodel.ErrCacheSchema, "bad cache JSON: %v", err)
	}
	if s.doc.Entries == nil {
		s.doc.Entries = map[string]Entry{}
	}
	return s, nil
}

// OpenInDir opens <dir>/cache.json.
func OpenInDir(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return Open(filepath.Join(dir, CacheFile))
}

// Get returns the cached entry for path.
func (s *Store) Get(path string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.doc.Entries[path]
	return e, ok
}

// ReplaceBaseline overwrites the path entry with exactly these fingerprints,
// used by the explicit -mutate demonstration to isolate one change axis.
func (s *Store) ReplaceBaseline(e Entry) error {
	s.mu.Lock()
	s.doc.Entries[e.Path] = e
	s.mu.Unlock()
	return s.save()
}

// Put merges and persists an entry.
func (s *Store) Put(e Entry) error {
	s.mu.Lock()
	if old, ok := s.doc.Entries[e.Path]; ok {
		if e.SummaryDigest == "" {
			e.SummaryDigest = old.SummaryDigest
		}
		if e.IndexSeq == 0 {
			e.IndexSeq = old.IndexSeq
		}
	}
	s.doc.Entries[e.Path] = e
	s.mu.Unlock()
	return s.save()
}

func (s *Store) save() error {
	s.mu.Lock()
	b, err := json.MarshalIndent(s.doc, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(s.path, b, 0o644); err != nil {
		return errmodel.New(errmodel.ErrCacheChecksum, "persist cache: %v", err)
	}
	return nil
}

// Snapshot returns a copy of all entries.
func (s *Store) Snapshot() Doc {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := Doc{Schema: s.doc.Schema, Entries: map[string]Entry{}}
	for k, v := range s.doc.Entries {
		cp.Entries[k] = v
	}
	return cp
}

// Check compares fingerprints against the cached path entry and returns the
// exact invalidation set under the agreed policy:
//
//	content change   -> parse-cache invalidated
//	structure change -> structure-cache invalidated only
//	metadata change  -> time-index invalidated
//
// A missing baseline returns nil (cold cache, nothing invalidated).
func Check(old Entry, found bool, now Fingerprints) []Invalidation {
	if !found {
		return nil
	}
	var out []Invalidation
	if old.Fingerprints.Content != now.Content {
		out = append(out, Invalidation{
			Object: "parse-cache", Reason: "content-fingerprint-mismatch",
			Old: old.Fingerprints.Content, New: now.Content,
		})
	}
	if old.Fingerprints.Structure != now.Structure {
		out = append(out, Invalidation{
			Object: "structure-cache", Reason: "structure-fingerprint-mismatch",
			Old: old.Fingerprints.Structure, New: now.Structure,
		})
	}
	if old.Fingerprints.Metadata != now.Metadata {
		out = append(out, Invalidation{
			Object: "time-index", Reason: "metadata-fingerprint-mismatch",
			Old: old.Fingerprints.Metadata, New: now.Metadata,
		})
	}
	return out
}
