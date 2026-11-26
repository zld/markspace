// Package recover isolates failed or corrupt segments and recomputes only the
// affected time range from the nearest verifiable (sync) boundary. Healthy
// segments keep their cached derived output; a genuinely corrupt source yields
// an unrecoverable range while every other result is preserved.
package recover

import (
	"fmt"
	"io"

	"mediad/internal/cache"
	"mediad/internal/timeline"
)

// Segment is one independently recoverable unit. Its byte range starts at a
// sync sample so it can be recomputed without decoding its neighbours.
type Segment struct {
	ID         int    `json:"id"`
	TrackID    uint32 `json:"track_id"`
	StartNanos int64  `json:"start_nanos"`
	EndNanos   int64  `json:"end_nanos"`
	Offset     int64  `json:"offset"`
	Length     int64  `json:"length"`
	Sync       bool   `json:"sync"`
	CacheKey   string `json:"cache_key"`
}

// SegmentSet groups a track's segments in time order.
type SegmentSet struct {
	TrackID  uint32    `json:"track_id"`
	Segments []Segment `json:"segments"`
}

// BuildSegments groups normalized samples into segments, starting a new segment
// at every sync sample (the verifiable recovery boundary), and derives each
// segment's content-addressed cache key from its own byte range.
func BuildSegments(n *timeline.Normalized, trackID uint32, params cache.Params, ra io.ReaderAt) (*SegmentSet, error) {
	set := &SegmentSet{TrackID: trackID}
	var cur *Segment
	segID := 0
	for _, t := range n.Tracks {
		if t.ID != trackID {
			continue
		}
		for _, s := range t.Samples {
			if s.IsSync || cur == nil {
				if cur != nil {
					set.Segments = append(set.Segments, *cur)
				}
				cur = &Segment{
					ID: segID, TrackID: trackID, StartNanos: s.StartNanos,
					EndNanos: s.EndNanos, Offset: s.Offset, Length: s.Length, Sync: true,
				}
				segID++
			} else {
				cur.EndNanos = s.EndNanos
				cur.Length += s.Length
			}
		}
		if cur != nil {
			set.Segments = append(set.Segments, *cur)
			cur = nil
		}
	}
	for i := range set.Segments {
		seg := &set.Segments[i]
		hash, err := cache.TrackContentHash(ra, []cache.ByteRange{{Offset: seg.Offset, Length: seg.Length}})
		if err != nil {
			return nil, fmt.Errorf("recover: segment %d content hash: %w", seg.ID, err)
		}
		key, err := cache.DerivedKey(hash, params)
		if err != nil {
			return nil, err
		}
		seg.CacheKey = key
	}
	return set, nil
}
