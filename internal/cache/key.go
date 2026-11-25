// Package cache builds content-addressed identities and holds the
// content cache. The three hashes are intentionally independent so that
// content replacement, container changes and metadata-only updates can
// invalidate different layers without touching the others.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
)

// Hash prefixes make the identity space self-describing in manifests.
const (
	PrefixContent   = "sha256:content:"
	PrefixContainer = "sha256:container:"
	PrefixMetadata  = "sha256:metadata:"
)

func digest(prefix string, parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return prefix + hex.EncodeToString(h.Sum(nil))
}

// ContentHash hashes only media payload bytes. RTP headers, transport
// metadata and filesystem paths are excluded, so identical media under
// a new path keeps the same content identity.
func ContentHash(payloads [][]byte) string {
	h := sha256.New()
	for _, p := range payloads {
		h.Write(p)
	}
	return PrefixContent + hex.EncodeToString(h.Sum(nil))
}

// ContainerHash hashes the encapsulation: codec, RTP header bytes and
// packetization structure. Same payload in a new container differs here
// while ContentHash stays stable.
func ContainerHash(codec string, headers [][]byte) string {
	parts := append([][]byte{[]byte(codec)}, headers...)
	return digest(PrefixContainer, parts...)
}

// MetadataHash hashes manifest-level metadata only (track id, clock
// rate, labels and similar). Updating labels changes this hash alone.
func MetadataHash(metadata []byte) string {
	return digest(PrefixMetadata, metadata)
}

// Entry is one cached content object keyed by its content hash.
type Entry struct {
	Key   string
	Value []byte
}

// Stats makes cache behavior observable for the acceptance scenarios.
type Stats struct {
	Hits          int
	Misses        int
	Invalidations int
}

// ContentCache is an in-memory content-addressed cache. It knows nothing
// about containers or metadata; those layers live in the index store.
type ContentCache struct {
	items map[string]Entry
	stats Stats
}

// NewContentCache creates an empty cache.
func NewContentCache() *ContentCache {
	return &ContentCache{items: make(map[string]Entry)}
}

// Lookup returns a cached value and whether it was present.
func (c *ContentCache) Lookup(key string) ([]byte, bool) {
	if e, ok := c.items[key]; ok {
		c.stats.Hits++
		return e.Value, true
	}
	c.stats.Misses++
	return nil, false
}

// Put stores a value under its content key.
func (c *ContentCache) Put(key string, value []byte) {
	cp := make([]byte, len(value))
	copy(cp, value)
	c.items[key] = Entry{Key: key, Value: cp}
}

// Invalidate removes one content key and reports whether anything was
// actually evicted. Content replacement goes through this path only;
// container or metadata changes must not call it.
func (c *ContentCache) Invalidate(key string) bool {
	if _, ok := c.items[key]; !ok {
		return false
	}
	delete(c.items, key)
	c.stats.Invalidations++
	return true
}

// Has reports membership without touching hit/miss counters.
func (c *ContentCache) Has(key string) bool {
	_, ok := c.items[key]
	return ok
}

// Stats returns a snapshot of the observable counters.
func (c *ContentCache) Stats() Stats { return c.stats }

// Len returns the number of cached entries.
func (c *ContentCache) Len() int { return len(c.items) }
