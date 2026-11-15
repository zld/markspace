// Package segment assembles normalized timeline parts into HLS media
// segments and low-latency partial segments. All boundaries are computed on
// the integer-millisecond axis; a boundary may only land on a fragment's
// sync sample, never inside a fragment.
package segment

import (
	"errors"
	"fmt"
	"path/filepath"

	"hlsforge/internal/ingest"
	"hlsforge/internal/timeline"
)

// MaxSegments caps how many segments one build may emit, bounding memory and
// disk usage for hostile or misconfigured inputs.
const MaxSegments = 4096

// MaxWindowMs is the largest rolling window the tool will accept, a safety
// bound against pathological configuration values.
const MaxWindowMs int64 = 60_000

// Part is one low-latency partial segment.
type Part struct {
	Index       int // part index inside its segment, 0-based
	PartSeq     int // global part sequence (== fragment sequence for fixtures)
	URI         string
	DurationMs  int64
	Gap         bool
	Independent bool // starts on a sync sample
}

// Segment groups a fixed number of parts.
type Segment struct {
	MediaSeq    int // HLS EXT-X-MEDIA-SEQUENCE value
	URI         string
	DurationMs  int64
	Parts       []Part
	HasGap      bool
	HasContent  bool
}

// TrackSegments is the fully sliced view of one bitrate.
type TrackSegments struct {
	Bitrate      int
	PartMs       int64
	SegmentMs    int64
	MediaPlaylist string
	SegmentDir   string
	Segments     []Segment
}

// Layout computes the segment/part structure for a normalized track. It does
// not touch the filesystem; callers persist bytes via the returned URIs. A
// non-zero windowMs applies a rolling window that keeps only the segments
// intersecting the trailing windowMs of the timeline, while MediaSeq stays
// absolute so dropped segments are never silently renumbered.
func Layout(t *timeline.Track, segmentMs, partMs, windowMs int64) (*TrackSegments, error) {
	if partMs <= 0 || segmentMs <= 0 || segmentMs%partMs != 0 {
		return nil, fmt.Errorf("segment: segment %dms must be a multiple of part %dms",
			segmentMs, partMs)
	}
	if windowMs < 0 || windowMs > MaxWindowMs {
		return nil, fmt.Errorf("segment: window %dms outside [0,%d]", windowMs, MaxWindowMs)
	}
	partsPerSegment := int(segmentMs / partMs)
	if len(t.Parts) > MaxSegments*partsPerSegment {
		return nil, fmt.Errorf("segment: %d parts exceed cap of %d",
			len(t.Parts), MaxSegments*partsPerSegment)
	}
	totalSegments := (len(t.Parts) + partsPerSegment - 1) / partsPerSegment
	firstSeg := 0
	if windowMs > 0 {
		keep := int(windowMs / segmentMs)
		if keep < totalSegments {
			firstSeg = totalSegments - keep
		}
	}
	out := &TrackSegments{
		Bitrate: t.Bitrate, PartMs: partMs, SegmentMs: segmentMs,
		MediaPlaylist: playlistName(t.Bitrate),
		SegmentDir:    fmt.Sprintf("%d", t.Bitrate),
	}
	// Every first part of a segment must start on a sync sample. Fixture
	// fragments are themselves sync boundaries, so a boundary is valid only
	// when the slot is filled or the whole leading region is still gapped.
	for segIdx := firstSeg; segIdx < totalSegments; segIdx++ {
		base := t.FromSeq + segIdx*partsPerSegment
		seg := Segment{
			MediaSeq: segIdx, // absolute sequence even under a rolling window
			URI:      segmentURI(t.Bitrate, segIdx),
		}
		for j := 0; j < partsPerSegment; j++ {
			seq := base + j
			if seq > t.ToSeq {
				break
			}
			part := t.Parts[partIndex(t, seq)]
			p := Part{
				Index: j, PartSeq: seq,
				URI:        partURI(t.Bitrate, segIdx, j),
				DurationMs: part.DurationMs,
				Gap:        part.Missing,
			}
			if part.Missing {
				// A gap is retained as an explicit GAP slot, never closed by
				// shifting later sequences forward.
				seg.HasGap = true
			} else {
				seg.HasContent = true
				// Every fixture fragment starts on an independently decodable
				// sample; a segment boundary must land on such a sync point.
				if j == 0 && !fragmentIsSync(part.Fragment) {
					return nil, fmt.Errorf("%w: segment %d starts on non-sync seq %d",
						ErrBoundaryNotSync, segIdx, seq)
				}
				p.Independent = fragmentIsSync(part.Fragment)
			}
			seg.Parts = append(seg.Parts, p)
			seg.DurationMs += part.DurationMs
		}
		out.Segments = append(out.Segments, seg)
	}
	return out, nil
}

// partIndex maps a fragment sequence to its position in the track parts.
func partIndex(t *timeline.Track, seq int) int {
	return seq - t.FromSeq
}

// fragmentIsSync reports whether a part's fragment starts on a sync sample.
func fragmentIsSync(f *ingest.Fragment) bool {
	return f != nil && f.Sync
}

func segmentURI(bitrate, segIdx int) string {
	return filepath.Join(fmt.Sprintf("%d", bitrate),
		fmt.Sprintf("seg-%05d.mp4", segIdx))
}

func partURI(bitrate, segIdx, partIdx int) string {
	return filepath.Join(fmt.Sprintf("%d", bitrate),
		fmt.Sprintf("seg-%05d-part-%d.mp4", segIdx, partIdx))
}

func playlistName(bitrate int) string {
	return fmt.Sprintf("%d.m3u8", bitrate)
}

// PlaylistName and PartURIName expose conventions to the manifest package.
func PlaylistName(bitrate int) string { return playlistName(bitrate) }

// PartCount returns the total number of emitted parts across all segments.
func (s *TrackSegments) PartCount() int {
	n := 0
	for _, seg := range s.Segments {
		n += len(seg.Parts)
	}
	return n
}

// GapPartCount returns how many emitted parts are unfilled gap slots.
func (s *TrackSegments) GapPartCount() int {
	n := 0
	for _, seg := range s.Segments {
		for _, p := range seg.Parts {
			if p.Gap {
				n++
		}
		}
	}
	return n
}

// ErrBoundaryNotSync means a segment boundary would fall inside a gap or a
// non-sync sample.
var ErrBoundaryNotSync = errors.New("segment: boundary is not a sync sample")
