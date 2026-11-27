// Package cache implements a content-addressable store with strict entry
// verification. A cache hit is honored only when the caller's source
// identity, source byte range and content digest all match the stored
// record; any mismatch invalidates the entry and forces recomputation, with
// the reason reported back to the caller.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/local/tracecheck/internal/clock"
)

// Source identifies the provenance of a cached item.
type Source struct {
	// InputID is a stable identity of the originating input (e.g. the
	// input file's own digest), so swapping the input invalidates entries.
	InputID string `json:"input_id"`
	// Start and End are the half-open source byte range [Start, End).
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Record is the on-disk metadata kept beside the cached blob.
type Record struct {
	Source      Source `json:"source"`
	SHA256      string `json:"sha256"`
	Size        int    `json:"size"`
	RecomputeBy string `json:"recompute_by,omitempty"`
}

// Mismatch explains why a lookup could not be served from cache.
type Mismatch struct {
	Key    string
	Reason clock.Code
	Detail string
}

// CAS is a content-addressable cache rooted at a directory.
type CAS struct {
	root string
	mu   sync.Mutex
	// Hits and Misses count lookup outcomes for observability.
	Hits    int
	Misses  int
	Evicted int
	// Mismatches records every verification failure and its reason.
	Mismatches []Mismatch
}

// Open creates or opens a cache rooted at dir.
func Open(dir string) (*CAS, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &CAS{root: dir}, nil
}

// Key derives the storage key from the content digest.
func Key(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func (c *CAS) blobPath(key string) string { return filepath.Join(c.root, key+".bin") }
func (c *CAS) metaPath(key string) string { return filepath.Join(c.root, key+".json") }

// Lookup verifies a candidate entry against the requested provenance and the
// actual content digest. A nil record means no usable cache entry (caller
// must recompute); Mismatch is non-nil whenever an existing entry was
// rejected, carrying the reason.
func (c *CAS) Lookup(src Source, content []byte) (*Record, *Mismatch) {
	key := Key(content)
	c.mu.Lock()
	defer c.mu.Unlock()

	metaBytes, err := os.ReadFile(c.metaPath(key))
	if err != nil {
		c.Misses++
		return nil, nil // no entry: ordinary miss, not a mismatch
	}
	var rec Record
	if err := json.Unmarshal(metaBytes, &rec); err != nil {
		c.evictLocked(key)
		c.Misses++
		m := &Mismatch{Key: key, Reason: clock.CodeCacheMismatch, Detail: "corrupt metadata"}
		c.Mismatches = append(c.Mismatches, *m)
		return nil, m
	}

	wantDigest := key
	switch {
	case rec.Source.InputID != src.InputID:
		m := c.rejectLocked(key, "input identity changed: cached=%s want=%s", rec.Source.InputID, src.InputID)
		return nil, m
	case rec.Source.Start != src.Start || rec.Source.End != src.End:
		m := c.rejectLocked(key, "source range changed: cached=[%d,%d) want=[%d,%d)",
			rec.Source.Start, rec.Source.End, src.Start, src.End)
		return nil, m
	case rec.SHA256 != wantDigest:
		m := c.rejectLocked(key, "content digest mismatch: cached=%s want=%s", rec.SHA256, wantDigest)
		return nil, m
	}

	// Re-hash the stored blob itself; tampered bytes must not be trusted.
	stored, err := os.ReadFile(c.blobPath(key))
	if err != nil {
		m := c.rejectLocked(key, "blob unreadable: %v", err)
		return nil, m
	}
	if Key(stored) != wantDigest {
		m := c.rejectLocked(key, "stored blob digest mismatch")
		return nil, m
	}
	c.Hits++
	return &rec, nil
}

// Put stores content and its provenance record, returning the key.
func (c *CAS) Put(src Source, content []byte) (string, error) {
	key := Key(content)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.WriteFile(c.blobPath(key), content, 0o644); err != nil {
		return "", err
	}
	rec := Record{Source: src, SHA256: key, Size: len(content)}
	meta, err := json.MarshalIndent(&rec, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(c.metaPath(key), meta, 0o644); err != nil {
		return "", err
	}
	return key, nil
}

// rejectLocked records a mismatch and evicts the bad entry.
func (c *CAS) rejectLocked(key, format string, args ...any) *Mismatch {
	detail := fmt.Sprintf(format, args...)
	c.evictLocked(key)
	c.Misses++
	m := Mismatch{Key: key, Reason: clock.CodeCacheMismatch, Detail: detail}
	c.Mismatches = append(c.Mismatches, m)
	return &m
}

func (c *CAS) evictLocked(key string) {
	_ = os.Remove(c.blobPath(key))
	_ = os.Remove(c.metaPath(key))
	c.Evicted++
}
