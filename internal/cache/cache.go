package cache

import (
	"image"
	"sync"

	"mediarepair/internal/source"
)

type Counters struct {
	DecodeHits             int `json:"decode_hits"`
	DecodeMisses           int `json:"decode_misses"`
	PackagingHits          int `json:"packaging_hits"`
	PackagingMisses        int `json:"packaging_misses"`
	PackagingInvalidations int `json:"packaging_invalidations"`
	IndexInvalidations     int `json:"index_invalidations"`
}

type Cache struct {
	mu sync.Mutex

	decoded map[string]image.Image

	packagingFingerprint string
	packagingTable       *source.SampleTable

	indexFingerprint string
	Counters         Counters
}

func New() *Cache {
	return &Cache{decoded: make(map[string]image.Image)}
}

// GetDecode returns a previously decoded frame only when its content
// fingerprint matches. Callers must not key this by path.
func (c *Cache) GetDecode(contentFP string) (image.Image, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	img, ok := c.decoded[contentFP]
	if ok {
		c.Counters.DecodeHits++
	} else {
		c.Counters.DecodeMisses++
	}
	return img, ok
}

func (c *Cache) PutDecode(contentFP string, img image.Image) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.decoded[contentFP] = img
}

// GetPackaging returns the parse/package cache only for an exact fingerprint.
// A changed fingerprint invalidates the old parsed table.
func (c *Cache) GetPackaging(fp string) (*source.SampleTable, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.packagingFingerprint == fp && c.packagingTable != nil {
		c.Counters.PackagingHits++
		return c.packagingTable, true
	}
	if c.packagingTable != nil && c.packagingFingerprint != fp {
		c.Counters.PackagingInvalidations++
	}
	c.Counters.PackagingMisses++
	return nil, false
}

func (c *Cache) PutPackaging(fp string, st *source.SampleTable) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.packagingFingerprint = fp
	c.packagingTable = st
}

// RequireIndexRebuild records metadata invalidation without touching decoded
// frame or packaging caches.
func (c *Cache) RequireIndexRebuild(metadataFP string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.indexFingerprint == metadataFP {
		return false
	}
	if c.indexFingerprint != "" {
		c.Counters.IndexInvalidations++
	}
	c.indexFingerprint = metadataFP
	return true
}

func (c *Cache) Snapshot() Counters {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Counters
}
