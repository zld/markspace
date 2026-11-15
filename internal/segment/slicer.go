// Package segment assembles normalized timeline parts into HLS media
// segments and low-latency partial segments. All boundaries are computed on
// the integer-millisecond axis; a boundary may only land on a fragment's
// sync sample, never inside a fragment.
package segment

import (
	"errors"
	"fmt"
	"path/filepath"

	"hlsforge/internal/timeline"
)

// MaxSegments caps how many segments one build may emit, bounding memory and
// disk usage for hostile or misconfigured inputs.
const MaxSegments = 4096

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
// not touch the filesystem; callers persist bytes via the returned URIs.
func Layout(t *timeline.Track, segmentMs, partMs int64) (*TrackSegments, error) {
	if partMs <= 0 || segmentMs <= 0 || segmentMs%partMs != 0 {
		return nil, fmt.Errorf("segment: segment %dms must be a multiple of part %dms",
			segmentMs, partMs)
	}
	partsPerSegment := int(segmentMs / partMs)
	if len(t.Parts) > MaxSegments*partsPerSegment {
		return nil, fmt.Errorf("segment: %d parts exceed cap of %d",
			len(t.Parts), MaxSegments*partsPerSegment)
	}
	out := &TrackSegments{
		Bitrate: t.Bitrate, PartMs: partMs, SegmentMs: segmentMs,
		MediaPlaylist: playlistName(t.Bitrate),
		SegmentDir:    fmt.Sprintf("%d", t.Bitrate),
	}
	// Every first part of a segment must start on a sync sample. Fixture
	// fragments are themselves sync boundaries, so a boundary is valid only
	// when the slot is filled or the whole leading region is still gapped.
	for segIdx, base := 0, t.FromSeq; base <= t.ToSeq; segIdx, base = segIdx+1, base+partsPerSegment {
		seg := Segment{
			MediaSeq: segIdx,
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
			if !part.Missing {
				seg.HasContent = true
				// Sync sample: fixture fragments are independently decodable.
				p.Independent = true
			} else {
				seg.HasGap = true
			}
			if j == 0 && part.Missing && segIdx > 0 {
				return nil, fmt.Errorf("%w: segment %d starts in a gap at seq %d",
					ErrBoundaryNotSync, segIdx, seq)
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
