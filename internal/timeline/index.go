package timeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"mediad/internal/scan"
)

// Index is the content-addressed description of one normalized source. It is
// addressed by three independent digests so that different changes invalidate
// exactly the right layer:
//   - SourceHash:   full-file SHA-256 of the source (any byte change);
//   - MetadataHash: SHA-256 of the extracted timed metadata (container/metadata
//     edits that leave media sample bytes unchanged);
//   - ParserVersion: parser semantics version (a parser upgrade forces a rebuild).
type Index struct {
	SourceHash    string `json:"source_hash"`
	MetadataHash  string `json:"metadata_hash"`
	ParserVersion string `json:"parser_version"`
	TrackCount    int    `json:"track_count"`
	SampleCount   int    `json:"sample_count"`
	EventCount    int    `json:"event_count"`
	// CacheKey identifies derived per-track caches; it changes only when the
	// track content or processing parameters change (see DerivedCacheKey).
	CacheKey string `json:"cache_key"`
}

// BuildIndex computes the index for a normalized timeline.
func BuildIndex(n *Normalized) (*Index, error) {
	metaHash, err := hashMetadata(n)
	if err != nil {
		return nil, err
	}
	sampleCount, eventCount := 0, 0
	for _, t := range n.Tracks {
		sampleCount += len(t.Samples)
		eventCount += len(t.Events)
	}
	return &Index{
		SourceHash:    n.SourceHash,
		MetadataHash:  metaHash,
		ParserVersion: scan.ParserVersion,
		TrackCount:    len(n.Tracks),
		SampleCount:   sampleCount,
		EventCount:    eventCount,
		CacheKey:      derivedCacheKey(n.SourceHash, metaHash, scan.ParserVersion),
	}, nil
}

// hashMetadata hashes a canonical JSON encoding of the tracks' timed metadata.
// JSON keys are fixed by struct tags and slices are pre-sorted, so equal
// metadata yields an equal hash regardless of map iteration order.
func hashMetadata(n *Normalized) (string, error) {
	type meta struct {
		Tracks []NormalizedTrack `json:"tracks"`
		Events []NormalizedEvent `json:"events"`
	}
	var allEvents []NormalizedEvent
	for i := range n.Tracks {
		sort.SliceStable(n.Tracks[i].Samples, func(a, b int) bool {
			return n.Tracks[i].Samples[a].Offset < n.Tracks[i].Samples[b].Offset
		})
		sort.SliceStable(n.Tracks[i].Events, func(a, b int) bool {
			return n.Tracks[i].Events[a].Offset < n.Tracks[i].Events[b].Offset
		})
		allEvents = append(allEvents, n.Tracks[i].Events...)
	}
	canonical, err := json.Marshal(meta{Tracks: n.Tracks, Events: allEvents})
	if err != nil {
		return "", fmt.Errorf("timeline: marshal metadata: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// derivedCacheKey combines source content, metadata digest and parser version
// with explicit domain separators so collisions across layers are impossible.
func derivedCacheKey(sourceHash, metadataHash, parserVersion string) string {
	h := sha256.New()
	fmt.Fprintf(h, "source=%s\x00metadata=%s\x00parser=%s", sourceHash, metadataHash, parserVersion)
	return hex.EncodeToString(h.Sum(nil))
}

// NeedsRebuild reports whether a cached index must be rebuilt for a freshly
// scanned source. A differing source hash or parser version always forces a
// rebuild; a metadata-only change (same source hash, new metadata hash) also
// rebuilds the index even though derived segment caches may be reused.
func NeedsRebuild(cached *Index, current *Index) bool {
	if cached == nil {
		return true
	}
	return cached.SourceHash != current.SourceHash ||
		cached.MetadataHash != current.MetadataHash ||
		cached.ParserVersion != current.ParserVersion
}
