package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"

	"media-audit/internal/source"
)

// KeySet contains content-addressed cache keys for one stable task.
type KeySet struct {
	TaskKey   string `json:"task_key"`
	Parsed    string `json:"parsed_key"`
	Structure string `json:"structure_key"`
	Metadata  string `json:"metadata_key"`
	Recovery  string `json:"recovery_key"`
	Manifest  string `json:"manifest_key"`
}

func digest(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

func KeysFor(taskKey string, fp source.Fingerprint) KeySet {
	return KeySet{
		TaskKey:   taskKey,
		Parsed:    "parse:" + taskKey + ":" + digest(fp.ContentSHA256, fp.StructureSHA256),
		Structure: "struct:" + taskKey + ":" + fp.StructureSHA256[:min(16, len(fp.StructureSHA256))],
		Metadata:  "meta:" + taskKey + ":" + digest(fp.MetadataVersion),
		Recovery:  "recover:" + taskKey + ":" + digest(fp.ContentSHA256, fp.StructureSHA256),
		Manifest:  "manifest:" + taskKey + ":" + digest(fp.ContentSHA256, fp.StructureSHA256, fp.MetadataVersion),
	}
}

// Store is an in-process cache with explicit invalidation. Persistent task
// progress lives in internal/task; this store only models safe reuse.
type Store struct {
	mu      sync.Mutex
	entries map[string]any
}

func NewStore() *Store { return &Store{entries: make(map[string]any)} }

func (s *Store) Put(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = value
}

func (s *Store) Get(key string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.entries[key]
	return v, ok
}

func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// InvalidateForChange applies the three fingerprint rules. It returns sorted
// removed keys for audit/test assertions.
func (s *Store) InvalidateForChange(keys KeySet, change source.ChangeSet) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	remove := map[string]bool{}
	switch {
	case change.ContentReplaced:
		for _, k := range []string{keys.Parsed, keys.Structure, keys.Metadata, keys.Recovery, keys.Manifest} {
			remove[k] = true
		}
	case change.StructureChanged:
		for _, k := range []string{keys.Parsed, keys.Structure, keys.Recovery, keys.Manifest} {
			remove[k] = true
		}
	case change.MetadataChanged:
		remove[keys.Metadata] = true
		remove[keys.Manifest] = true
	}
	var removed []string
	for k := range remove {
		if _, ok := s.entries[k]; ok {
			delete(s.entries, k)
			removed = append(removed, k)
		}
	}
	sort.Strings(removed)
	return removed
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
