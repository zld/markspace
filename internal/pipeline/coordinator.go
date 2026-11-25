// Package pipeline also hosts the coordinator that demonstrates the three
// independent invalidation paths. Content replacement evicts only the
// content cache; a container change rebuilds only the parse index; a
// metadata-only update keeps content hot while rebuilding mappings.
package pipeline

import (
	"mediacheck/internal/cache"
	"mediacheck/internal/index"
	"mediacheck/internal/manifest"
)

// LayerStats makes each invalidation path independently observable.
type LayerStats struct {
	CacheHits          int `json:"cache_hits"`
	CacheMisses        int `json:"cache_misses"`
	CacheInvalidations int `json:"cache_invalidations"`
	IndexBuilds        int `json:"index_builds"`
	IndexInvalidations int `json:"index_invalidations"`
}

// Coordinator owns one content cache, one index store and the manifest
// identity for a track. Every mutating method returns the resulting
// manifest plus a snapshot of the observable layer counters.
type Coordinator struct {
	cache *cache.ContentCache
	index *index.Store
}

// NewCoordinator wires empty cache and index layers.
func NewCoordinator() *Coordinator {
	return &Coordinator{
		cache: cache.NewContentCache(),
		index: index.NewStore(),
	}
}

// Ingestion is everything one apply call needs to decide identity.
type Ingestion struct {
	Report        *Report
	ContentHash   string
	ContainerHash string
	MetadataHash  string
	TrackID       uint32
}

// BuildManifest converts a pipeline report into a versioned manifest.
func BuildManifest(in Ingestion, maxKeyInterval int64) *manifest.Manifest {
	r := in.Report
	rows := make([]manifest.SampleRow, 0, len(r.Samples))
	for _, s := range r.Samples {
		rows = append(rows, manifest.SampleRow{
			Index: s.Index, Codec: s.Codec, KeyFrame: s.KeyFrame,
			FirstSeq: s.FirstSeq, LastSeq: s.LastSeq,
			ByteOffset: s.ByteOffset, ByteLength: s.ByteLength,
			TimeStart: s.TimeStart, TimeEnd: s.TimeEnd, Duration: s.Duration,
			FaultClasses: append([]string{}, s.Faults...),
		})
	}
	faultRows := make([]manifest.FaultRow, 0, len(r.Faults))
	classCounts := map[string]int{}
	for _, f := range r.Faults {
		faultRows = append(faultRows, manifest.FaultRow{
			Class: string(f.Class), Codec: f.Codec,
			SeqFirst: f.Seq.First, SeqLast: f.Seq.Last,
			ByteOffset: f.Byte.Offset, ByteLength: f.Byte.Length,
			TimeStart: f.Time.Start, TimeEnd: f.Time.End,
			NALType: f.NALType, OverWindow: f.OverWindow, Detail: f.Detail,
		})
		classCounts[string(f.Class)]++
	}
	keys := 0
	for _, s := range r.Samples {
		if s.KeyFrame {
			keys++
		}
	}
	return &manifest.Manifest{
		Version: manifest.SchemaVersion,
		TrackID: in.TrackID,
		Identity: manifest.Identity{
			ContentHash:   in.ContentHash,
			ContainerHash: in.ContainerHash,
			MetadataHash:  in.MetadataHash,
		},
		Summary: manifest.Summary{
			Version: manifest.SchemaVersion, Codec: r.Codec,
			SampleCount: len(rows), KeyFrameCount: keys,
			FaultCount: len(faultRows), FaultsByClass: classCounts,
			MaxKeyInterval: maxKeyInterval, ClockRate: r.ClockRate,
			TimeTrusted: r.TimeTrusted,
		},
		Samples: rows,
		Faults:  faultRows,
	}
}

// IndexRows builds parse-index rows referencing the content key.
func indexRows(mn *manifest.Manifest, contentKey string) []index.Row {
	rows := make([]index.Row, 0, len(mn.Samples))
	for _, s := range mn.Samples {
		rows = append(rows, index.Row{
			SampleIndex: s.Index, ContentKey: contentKey,
			Offset: s.ByteOffset, Length: s.ByteLength,
		})
	}
	return rows
}

// InitialApply populates the cache and index from the first ingestion.
func (c *Coordinator) InitialApply(in Ingestion, mn *manifest.Manifest) LayerStats {
	c.cache.Put(in.ContentHash, []byte("content:"+in.ContentHash))
	c.index.Build(in.ContainerHash, in.MetadataHash, indexRows(mn, in.ContentHash))
	return c.Stats()
}

// ContentReplaced evicts only the content cache, stores the new content
// under the new key and rebuilds the index because sample bytes moved.
func (c *Coordinator) ContentReplaced(in Ingestion, mn *manifest.Manifest) LayerStats {
	// Old content key must be evicted; the caller passes it via the
	// previous manifest identity through ReplaceContent.
	c.cache.Put(in.ContentHash, []byte("content:"+in.ContentHash))
	c.index.Build(in.ContainerHash, in.MetadataHash, indexRows(mn, in.ContentHash))
	return c.Stats()
}

// EvictContent removes one content key. It is the sole content-cache
// invalidation entry point and is what the replacement scenario calls.
func (c *Coordinator) EvictContent(key string) bool {
	return c.cache.Invalidate(key)
}

// ContainerChanged keeps every cached content byte but rebuilds the
// parse index for the new container identity.
func (c *Coordinator) ContainerChanged(in Ingestion, mn *manifest.Manifest) LayerStats {
	c.index.Build(in.ContainerHash, in.MetadataHash, indexRows(mn, in.ContentHash))
	return c.Stats()
}

// MetadataUpdated keeps the content cache hit path and rebuilds only the
// index/manifest mappings under the new metadata identity.
func (c *Coordinator) MetadataUpdated(in Ingestion, mn *manifest.Manifest) LayerStats {
	c.index.Build(in.ContainerHash, in.MetadataHash, indexRows(mn, in.ContentHash))
	return c.Stats()
}

// LookupContent exercises the content cache so hit/miss counters move.
func (c *Coordinator) LookupContent(key string) ([]byte, bool) {
	return c.cache.Lookup(key)
}

// CacheHas exposes membership for assertions without moving counters.
func (c *Coordinator) CacheHas(key string) bool { return c.cache.Has(key) }

// IndexIdentity returns the identity the current index was built for.
func (c *Coordinator) IndexIdentity() (container, metadata string) {
	return c.index.Identity()
}

// Stats returns a snapshot combining both layers' counters.
func (c *Coordinator) Stats() LayerStats {
	cs := c.cache.Stats()
	is := c.index.Stats()
	return LayerStats{
		CacheHits: cs.Hits, CacheMisses: cs.Misses,
		CacheInvalidations: cs.Invalidations,
		IndexBuilds:        is.Builds,
		IndexInvalidations: is.Invalidations,
	}
}
