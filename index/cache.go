package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// CacheEntry is the persisted parse of one input's timed metadata. It is keyed
// by the metadata fingerprint, so a remux that changes container layout but not
// metadata keeps its events while a metadata edit drops them even when the
// container digest is unchanged.
type CacheEntry struct {
	Path       string `json:"path"`
	Metadata   string `json:"metadata_sha256"`
	Version    int    `json:"parser_version"`
	EventsJSON []byte `json:"events"` // opaque to the cache; owned by media
	IssuesJSON []byte `json:"issues,omitempty"`
	Created    int64  `json:"created_unix"`
}

// Cache stores metadata parse results independently of the parse index, so the
// two can be invalidated on different triggers.
type Cache struct {
	dir string
	mu  sync.Mutex
}

// OpenCache opens or creates the cache directory.
func OpenCache(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	return &Cache{dir: dir}, nil
}

func (c *Cache) pathFor(path string) string {
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:16])+".meta.json")
}

// Get returns the entry for path only if it matches the metadata fingerprint
// and parser version. A mismatch is a cache miss; the stale file is removed so
// it cannot be picked up later by a buggy path.
func (c *Cache) Get(path, metadataSum string, version int) (*CacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := os.ReadFile(c.pathFor(path))
	if err != nil {
		return nil, false
	}
	var e CacheEntry
	if err := json.Unmarshal(data, &e); err != nil || e.Path != path {
		os.Remove(c.pathFor(path))
		return nil, false
	}
	if e.Metadata != metadataSum || e.Version != version {
		os.Remove(c.pathFor(path))
		return nil, false
	}
	return &e, true
}

// Put stores the entry atomically.
func (c *Cache) Put(e *CacheEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.Metadata == "" {
		return errors.New("index: cache entry without metadata fingerprint")
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return writeAtomic(c.pathFor(e.Path), data)
}

// Invalidate removes the cached metadata for path.
func (c *Cache) Invalidate(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := os.Remove(c.pathFor(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
