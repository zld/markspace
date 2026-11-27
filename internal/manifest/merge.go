// Package manifest merges per-task results into a deterministic
// manifest: entries are keyed by a stable task key, deduplicated on
// retry, sorted, and hashed so identical inputs always produce the
// identical manifest SHA-256.
package manifest

import (
	"crypto/sha256"
	"encoding/json"
	"sort"

	"mediarebuild/internal/model"
)

// Entry is one task's contribution to the manifest.
type Entry struct {
	Key         string            `json:"key"`
	Kind        string            `json:"kind"`
	Metrics     map[string]uint64 `json:"metrics,omitempty"`
	Hashes      map[string]string `json:"hashes,omitempty"`
	Diagnostics model.Diags       `json:"diagnostics,omitempty"`
}

// Manifest is the final, deterministically ordered document.
type Manifest struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Merger accumulates entries by stable key.
type Merger struct {
	byKey map[string]Entry
}

// NewMerger creates an empty merger.
func NewMerger() *Merger { return &Merger{byKey: map[string]Entry{}} }

// Add inserts or replaces the entry for its key. Re-adding the same key
// (e.g. after a failed retry) overwrites rather than accumulates, so
// counts are never double-added.
func (m *Merger) Add(e Entry) {
	if e.Key == "" {
		panic("manifest: entry key must not be empty")
	}
	m.byKey[e.Key] = e
}

// Len returns the number of distinct keys.
func (m *Merger) Len() int { return len(m.byKey) }

// Finalize sorts entries by key and returns the manifest with its
// canonical SHA-256. The same entry set always yields the same hash.
func (m *Merger) Finalize() (Manifest, [32]byte) {
	keys := make([]string, 0, len(m.byKey))
	for k := range m.byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	man := Manifest{Version: 1, Entries: make([]Entry, 0, len(keys))}
	for _, k := range keys {
		man.Entries = append(man.Entries, m.byKey[k])
	}
	return man, Hash(man)
}

// Hash computes the canonical SHA-256 of a manifest. encoding/json
// emits map keys in sorted order, so the encoding is deterministic.
func Hash(man Manifest) [32]byte {
	data, err := json.Marshal(man)
	if err != nil {
		panic(err)
	}
	return sha256.Sum256(data)
}
