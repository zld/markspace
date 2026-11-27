// Package index builds the random-access sample index over decoded Matroska
// blocks. A record keeps the block's track, media time, cluster position and
// sample offset, so any half-open time range can be served from the original
// bytes without re-parsing the file.
package index

import (
	"sort"

	"tracecheck/internal/clock"
	"tracecheck/internal/mkv"
	"tracecheck/internal/serr"
)

// Record is one indexed block on the unified nanosecond timeline.
type Record struct {
	Track        uint64
	BlockIndex   int   // stable order within the whole document
	TimeNanos    int64 // mapped media time of the block
	Ticks        int64 // raw Matroska ticks, preserved for exactness
	ClusterIndex int
	ClusterStart int64 // absolute offset of the Cluster element
	ClusterEnd   int64 // exclusive end of the Cluster element
	SampleStart  int64 // absolute offset of the frame payload
	SampleEnd    int64 // exclusive end of the block element
	Keyframe     bool
}

// TrackIndex groups records for one track in time order plus the keyframe
// subset used to satisfy keyframe lookups.
type TrackIndex struct {
	Track     uint64
	Records   []Record
	Keyframes []int // indices into Records whose Keyframe flag is set
}

// Index is the complete per-track sample index.
type Index struct {
	Timebase clock.Timebase
	Tracks   []*TrackIndex
	Scale    string // TimestampScale description recorded in the digest
	// Clusters maps cluster ordinal to its byte span for range planning.
	Clusters []ClusterSpan
}

// ClusterSpan is the byte range and time span of one Cluster.
type ClusterSpan struct {
	Index     int
	Start     int64
	End       int64
	TimeNanos int64
}

// Build converts the ordered blocks into an index. Blocks are sorted by the
// canonical (time, track, index) order first so re-running on identical input
// produces an identical index and therefore an identical digest.
func Build(blocks []mkv.Block, tb clock.Timebase, tickNanos int64, scaleLabel string) (*Index, error) {
	if err := tb.Validate(); err != nil {
		return nil, err
	}
	ordered := append([]mkv.Block(nil), blocks...)
	mkv.SortStable(ordered)

	idx := &Index{Timebase: tb, Scale: scaleLabel}
	byTrack := map[uint64]*TrackIndex{}
	clusters := map[int]ClusterSpan{}
	for _, b := range ordered {
		ticks := b.TimecodeTicks()
		ns, err := tb.TicksToNanos(ticks)
		if err != nil {
			return nil, err
		}
		rec := Record{
			Track:        b.TrackNumber,
			BlockIndex:   b.Index,
			TimeNanos:    ns,
			Ticks:        ticks,
			ClusterIndex: b.Cluster.Index,
			ClusterStart: b.Cluster.Offset,
			ClusterEnd:   b.Cluster.End,
			SampleStart:  b.PayloadStart,
			SampleEnd:    b.PayloadEnd,
			Keyframe:     b.Keyframe,
		}
		ti := byTrack[b.TrackNumber]
		if ti == nil {
			ti = &TrackIndex{Track: b.TrackNumber}
			byTrack[b.TrackNumber] = ti
			idx.Tracks = append(idx.Tracks, ti)
		}
		ti.Records = append(ti.Records, rec)
		if b.Keyframe {
			ti.Keyframes = append(ti.Keyframes, len(ti.Records)-1)
		}
		if _, seen := clusters[b.Cluster.Index]; !seen {
			clusters[b.Cluster.Index] = ClusterSpan{
				Index:     b.Cluster.Index,
				Start:     b.Cluster.Offset,
				End:       b.Cluster.End,
				TimeNanos: ns,
			}
		}
	}
	for _, ti := range idx.Tracks {
		sort.SliceStable(ti.Records, func(i, j int) bool {
			if ti.Records[i].TimeNanos != ti.Records[j].TimeNanos {
				return ti.Records[i].TimeNanos < ti.Records[j].TimeNanos
			}
			return ti.Records[i].BlockIndex < ti.Records[j].BlockIndex
		})
		// Recompute keyframe positions after the per-track sort.
		ti.Keyframes = ti.Keyframes[:0]
		for i := range ti.Records {
			if ti.Records[i].Keyframe {
				ti.Keyframes = append(ti.Keyframes, i)
			}
		}
	}
	sort.SliceStable(idx.Tracks, func(i, j int) bool {
		return idx.Tracks[i].Track < idx.Tracks[j].Track
	})
	for c := range clusters {
		idx.Clusters = append(idx.Clusters, clusters[c])
	}
	sort.SliceStable(idx.Clusters, func(i, j int) bool {
		return idx.Clusters[i].Index < idx.Clusters[j].Index
	})
	return idx, nil
}

// Track returns the index for a track number, or nil.
func (ix *Index) Track(n uint64) *TrackIndex {
	for _, ti := range ix.Tracks {
		if ti.Track == n {
			return ti
		}
	}
	return nil
}

// Span returns the earliest and latest media time across all tracks.
func (ix *Index) Span() (int64, int64, error) {
	if len(ix.Tracks) == 0 {
		return 0, 0, serr.New(serr.CodeRangeOutOfBounds, "index has no records")
	}
	minNs, maxNs := int64(0), int64(0)
	first := true
	for _, ti := range ix.Tracks {
		if len(ti.Records) == 0 {
			continue
		}
		lo := ti.Records[0].TimeNanos
		hi := ti.Records[len(ti.Records)-1].TimeNanos
		if first {
			minNs, maxNs, first = lo, hi, false
			continue
		}
		if lo < minNs {
			minNs = lo
		}
		if hi > maxNs {
			maxNs = hi
		}
	}
	if first {
		return 0, 0, serr.New(serr.CodeRangeOutOfBounds, "index has no records")
	}
	return minNs, maxNs, nil
}

// KeyframeBefore returns the index of the last keyframe at or before t, or -1.
// It is the look-up used to satisfy keyframe_hit_rate: every requested time
// must resolve to a keyframe no later than itself.
func (ti *TrackIndex) KeyframeBefore(t int64) int {
	pos := -1
	for _, k := range ti.Keyframes {
		if ti.Records[k].TimeNanos <= t {
			pos = k
		} else {
			break
		}
	}
	return pos
}

// KeyframeAt reports whether a keyframe starts exactly at t.
func (ti *TrackIndex) KeyframeAt(t int64) (int, bool) {
	k := ti.KeyframeBefore(t)
	if k >= 0 && ti.Records[k].TimeNanos == t {
		return k, true
	}
	return -1, false
}

// Digest renders a stable textual digest of the index used by checkpoints and
// the demo summary. It contains no absolute paths or times of day.
func (ix *Index) Digest() string {
	h := newHasher()
	h.addString(ix.Scale)
	h.addInt(ix.Timebase.TickNanos)
	for _, ti := range ix.Tracks {
		h.addInt(int64(ti.Track))
		h.addInt(int64(len(ti.Records)))
		h.addInt(int64(len(ti.Keyframes)))
		for _, r := range ti.Records {
			h.addInt(int64(r.BlockIndex))
			h.addInt(r.TimeNanos)
			h.addInt(r.Ticks)
			h.addInt(int64(r.ClusterIndex))
			h.addInt(r.ClusterStart)
			h.addInt(r.SampleStart)
			h.addInt(r.SampleEnd)
			if r.Keyframe {
				h.addString("K")
			} else {
				h.addString("D")
			}
		}
	}
	return h.hex()
}
