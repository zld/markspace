// Package runner streams segments through the configured pipeline, manages
// the derived-artifact cache and writes the machine-readable report.
package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"archive-lab/internal/media"
)

// CacheState is how a run's cache entry resolved.
type CacheState string

const (
	CacheHit      CacheState = "hit"
	CacheMiss     CacheState = "miss"
	CacheDisabled CacheState = "disabled"
)

// Entry is one cache record. Key is derived from the input digest and the
// configuration digest; the two halves are stored separately so that a
// configuration change invalidates the entry while leaving the input index
// untouched, and vice versa.
type Entry struct {
	Key         string `json:"key"`
	InputDigest string `json:"input_digest"`
	ConfigHash  string `json:"config_digest"`
	// SegmentDigests records the content of every segment the entry was
	// produced from. A changed segment always produces a different key, but
	// this is also checked on read to defend against key collisions.
	SegmentDigests map[string]string `json:"segment_digests"`
	SegmentsDone   int               `json:"segments_done"`
	SegmentsTotal  int               `json:"segments_total"`
	Bytes          int64             `json:"bytes"`
	ReportDigest   string            `json:"report_digest"`
	Artifact       string            `json:"artifact"`
}

// Cache is a directory of JSON entries keyed by the pair of digests. It never
// stores or deletes input indexes; callers own those.
type Cache struct {
	Dir string
}

// Key derives the cache key from the two independent digests.
func Key(inputDigest, configDigest string) string {
	h := sha256.New()
	fmt.Fprintf(h, "input=%s\nconfig=%s\n", inputDigest, configDigest)
	return hex.EncodeToString(h.Sum(nil))
}

// Load looks up an entry. A cache entry whose recorded segment digests no
// longer match the live index is treated as a miss, and so is a corrupt file.
func (c *Cache) Load(key string, segmentDigests map[string]string) (*Entry, CacheState, error) {
	if c == nil || c.Dir == "" {
		return nil, CacheDisabled, nil
	}
	path := c.entryPath(key)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, CacheMiss, nil
	}
	if err != nil {
		return nil, CacheMiss, media.Wrap(media.StageCache, "cache", err, "cannot read cache entry").WithPath(path)
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil || e.Key != key {
		// Corrupt entries are a miss, not a hard failure: the pipeline can
		// always recompute.
		os.Remove(path)
		return nil, CacheMiss, nil
	}
	if e.InputDigest == "" || e.ConfigHash == "" || e.SegmentsDone <= 0 || e.SegmentsDone > e.SegmentsTotal {
		os.Remove(path)
		return nil, CacheMiss, nil
	}
	// A partially completed entry records resume progress, not a reusable
	// result; only a complete run counts as a hit.
	if e.SegmentsDone != e.SegmentsTotal || e.ReportDigest == "" {
		return nil, CacheMiss, nil
	}
	for rel, want := range segmentDigests {
		if got, ok := e.SegmentDigests[rel]; !ok || got != want {
			return nil, CacheMiss, nil
		}
	}
	return &e, CacheHit, nil
}

// Store writes an entry atomically.
func (c *Cache) Store(e Entry) error {
	if c == nil || c.Dir == "" {
		return nil
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return media.Wrap(media.StageCache, "cache", err, "create cache directory").WithPath(c.Dir)
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return media.Wrap(media.StageCache, "cache", err, "marshal cache entry")
	}
	path := c.entryPath(e.Key)
	tmp, err := os.CreateTemp(c.Dir, ".cache-*.tmp")
	if err != nil {
		return media.Wrap(media.StageCache, "cache", err, "create temp cache entry").WithPath(c.Dir)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return media.Wrap(media.StageCache, "cache", err, "write temp cache entry").WithPath(name)
	}
	if err := tmp.Close(); err != nil {
		return media.Wrap(media.StageCache, "cache", err, "close temp cache entry").WithPath(name)
	}
	if err := os.Rename(name, path); err != nil {
		return media.Wrap(media.StageCache, "cache", err, "commit cache entry").WithPath(path)
	}
	return nil
}

// Invalidate removes one key.
func (c *Cache) Invalidate(key string) error {
	if c == nil || c.Dir == "" {
		return nil
	}
	err := os.Remove(c.entryPath(key))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return media.Wrap(media.StageCache, "cache", err, "invalidate entry").WithPath(c.entryPath(key))
	}
	return nil
}

// PurgeForInput drops every entry produced from inputDigest, leaving entries
// for other inputs (and their indexes) untouched. This is the only removal
// path the runner uses when inputs change.
func (c *Cache) PurgeForInput(inputDigest string) (int, error) {
	if c == nil || c.Dir == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, media.Wrap(media.StageCache, "cache", err, "list cache").WithPath(c.Dir)
	}
	removed := 0
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(c.Dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var e Entry
		if json.Unmarshal(data, &e) != nil || e.InputDigest != inputDigest {
			continue
		}
		if err := os.Remove(path); err == nil {
			removed++
		}
	}
	return removed, nil
}

func (c *Cache) entryPath(key string) string {
	return filepath.Join(c.Dir, key+".json")
}
