// Package align cuts the reconstructed timeline into fixed-duration
// segments and aligns multiple bitrate representation layers on a shared
// integer-nanosecond grid. Segments whose cross-layer skew exceeds the
// configured threshold are marked failed and excluded from valid delivery.
package align

import (
	"github.com/local/tracecheck/internal/clock"
	"github.com/local/tracecheck/internal/mkv"
	"github.com/local/tracecheck/internal/rtptrace"
)

// Layer is one bitrate representation derived from a track.
type Layer struct {
	Bitrate     uint32
	TrackNumber uint64
}

// Segment is one aligned output window.
type Segment struct {
	Index   int
	StartNS int64
	EndNS   int64
	// BlockSourceOffsets lists every block that falls in the window,
	// across all layers, in sorted timeline order.
	BlockSourceOffsets []int64
	// LayerCoverage maps track number to its block count in this window.
	LayerCoverage map[uint64]int
	// SkewNS is the maximum absolute time difference between any two
	// representation-layer anchor blocks inside the window.
	SkewNS   clock.Tick
	OK       bool
	FailCode clock.Code
}

// Config controls segmentation and the alignment threshold.
type Config struct {
	SegmentNS int64
	// MaxSkewNS is the business threshold; a segment exceeding it fails.
	MaxSkewNS clock.Tick
	Layers    []Layer
}

// Result holds the cut segments and aggregate metrics.
type Result struct {
	Segments  []*Segment
	Aligned   int
	MaxSkewNS clock.Tick
	// FailedRanges lists the [start,end) windows that failed alignment.
	FailedRanges [][2]int64
}

// Cut partitions sorted blocks into fixed windows starting at the timeline
// origin and aligns the configured layers.
func Cut(blocks []*mkv.Block, trace *rtptrace.Trace, cfg Config) *Result {
	res := &Result{}
	if len(blocks) == 0 || cfg.SegmentNS <= 0 {
		return res
	}
	origin := blocks[0].AbsTime.Nanoseconds()
	lastEnd := blocks[len(blocks)-1].AbsTime.Nanoseconds() + 1
	count := int((lastEnd-origin)/cfg.SegmentNS) + 1
	segs := make([]*Segment, count)
	for i := range segs {
		segs[i] = &Segment{
			Index:         i,
			StartNS:       origin + int64(i)*cfg.SegmentNS,
			EndNS:         origin + int64(i+1)*cfg.SegmentNS,
			LayerCoverage: map[uint64]int{},
		}
	}

	// Anchor time per segment per layer, used to compute cross-layer skew.
	anchors := make([]map[uint64]clock.Tick, count)
	for i := range anchors {
		anchors[i] = map[uint64]clock.Tick{}
	}
	layerTracks := map[uint64]bool{}
	for _, l := range cfg.Layers {
		layerTracks[l.TrackNumber] = true
	}

	for _, b := range blocks {
		idx := int((b.AbsTime.Nanoseconds() - origin) / cfg.SegmentNS)
		if idx < 0 || idx >= count {
			continue
		}
		seg := segs[idx]
		seg.BlockSourceOffsets = append(seg.BlockSourceOffsets, b.SourceOffset)
		seg.LayerCoverage[b.TrackNumber]++
		if layerTracks[b.TrackNumber] {
			if _, ok := anchors[idx][b.TrackNumber]; !ok {
				anchors[idx][b.TrackNumber] = b.AbsTime
			}
		}
	}

	for i, seg := range segs {
		skew := maxAnchorSpread(anchors[i])
		seg.SkewNS = skew
		if skew > cfg.MaxSkewNS {
			seg.OK = false
			seg.FailCode = clock.CodeAlignmentSkew
			res.FailedRanges = append(res.FailedRanges, [2]int64{seg.StartNS, seg.EndNS})
		} else {
			seg.OK = true
			res.Aligned++
		}
		if skew > res.MaxSkewNS {
			res.MaxSkewNS = skew
		}
	}
	res.Segments = segs
	_ = trace
	return res
}

// maxAnchorSpread returns the difference between the latest and earliest
// layer anchors within one segment.
func maxAnchorSpread(a map[uint64]clock.Tick) clock.Tick {
	if len(a) < 2 {
		return 0
	}
	var lo, hi clock.Tick
	first := true
	for _, t := range a {
		if first {
			lo, hi = t, t
			first = false
			continue
		}
		lo = clock.Min(lo, t)
		hi = clock.Max(hi, t)
	}
	return hi - lo
}
