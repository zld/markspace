package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CacheEntry is the metadata stored beside a cached manifest. It records the
// three facts that must all match for a hit: input provenance, config digest
// and the cached content hash.
type CacheEntry struct {
	InputDigest  string `json:"input_digest"`
	ConfigDigest string `json:"config_digest"`
	ContentHash  string `json:"content_hash"`
}

// CacheHit reports whether a lookup resolved to verified content.
type CacheHit struct {
	Found       bool
	Quarantined bool
	Content     []byte
	Reason      string
}

// Cache is a content-addressed store keyed by input+config digest.
type Cache struct {
	Dir string
}

// Key returns the cache key for an input and config.
func (c Cache) Key(inputDigest, configDigest string) string {
	sum := sha256.Sum256([]byte(inputDigest + "\x00" + configDigest))
	return hex.EncodeToString(sum[:])
}

func (c Cache) entryPath(key string) string   { return filepath.Join(c.Dir, key+".json") }
func (c Cache) contentPath(key string) string { return filepath.Join(c.Dir, key+".manifest") }

// Get looks up content by key and verifies provenance, config and content hash
// together. Any mismatch quarantines the entry and reports a miss, so a stale
// or tampered cache can never be used.
func (c Cache) Get(inputDigest, configDigest string) (CacheHit, error) {
	key := c.Key(inputDigest, configDigest)
	entryBytes, err := os.ReadFile(c.entryPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return CacheHit{Found: false, Reason: "no entry"}, nil
	}
	if err != nil {
		return CacheHit{}, err
	}
	var entry CacheEntry
	if err := json.Unmarshal(entryBytes, &entry); err != nil {
		return c.quarantine(key, "corrupt entry metadata")
	}
	switch {
	case entry.InputDigest != inputDigest:
		return c.quarantine(key, "input digest mismatch")
	case entry.ConfigDigest != configDigest:
		return c.quarantine(key, "config digest mismatch")
	}
	content, err := os.ReadFile(c.contentPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return c.quarantine(key, "cached content missing")
	}
	if err != nil {
		return CacheHit{}, err
	}
	sum := sha256.Sum256(content)
	if got := hex.EncodeToString(sum[:]); got != entry.ContentHash {
		return c.quarantine(key, "content hash mismatch")
	}
	return CacheHit{Found: true, Content: content}, nil
}

// Put stores content and its provenance metadata atomically.
func (c Cache) Put(inputDigest, configDigest string, content []byte) error {
	key := c.Key(inputDigest, configDigest)
	sum := sha256.Sum256(content)
	entry := CacheEntry{
		InputDigest:  inputDigest,
		ConfigDigest: configDigest,
		ContentHash:  hex.EncodeToString(sum[:]),
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	// Content first, then metadata: a crash between the two leaves an
	// unreferenced blob, never a metadata record pointing at nothing.
	if err := writeAtomic(c.contentPath(key), content); err != nil {
		return err
	}
	return writeAtomic(c.entryPath(key), b)
}

// quarantine moves a bad entry and its content aside. Quarantined files are
// kept for inspection but never served.
func (c Cache) quarantine(key, reason string) (CacheHit, error) {
	qdir := filepath.Join(c.Dir, "quarantine")
	if err := os.MkdirAll(qdir, 0o755); err != nil {
		return CacheHit{}, err
	}
	moved := false
	for _, p := range []string{c.entryPath(key), c.contentPath(key)} {
		if err := os.Rename(p, filepath.Join(qdir, filepath.Base(p))); err == nil {
			moved = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return CacheHit{}, fmt.Errorf("pipeline: quarantine %s: %w", p, err)
		}
	}
	hit := CacheHit{Found: false, Quarantined: moved, Reason: reason}
	return hit, nil
}

// QuarantineCount returns how many files have been set aside.
func (c Cache) QuarantineCount() int {
	entries, err := os.ReadDir(filepath.Join(c.Dir, "quarantine"))
	if err != nil {
		return 0
	}
	return len(entries)
}
