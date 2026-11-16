// Package hls defines the playlist data model and a self-contained
// M3U8 encoder/decoder: an RFC 8216 media-playlist subset plus custom
// per-segment sequence and SHA-256 tags.
package hls

import "math"

// Segment describes one media segment entry in a playlist.
type Segment struct {
	Seq      int     // explicit media sequence number
	URI      string  // relative file name, no directory traversal
	Duration float64 // seconds, EXTINF value
	SHA256   string  // lowercase hex, 64 chars
}

// Playlist is the in-memory model of an HLS media playlist.
type Playlist struct {
	Version        int
	TargetDuration int // seconds, ceiling of the max segment duration
	MediaSequence  int
	Segments       []Segment
	EndList        bool
}

// New builds a playlist from segment metadata, deriving the target
// duration from the longest segment.
func New(mediaSeq int, segs []Segment) *Playlist {
	p := &Playlist{Version: 3, MediaSequence: mediaSeq, Segments: segs, EndList: true}
	max := 0.0
	for _, s := range segs {
		if s.Duration > max {
			max = s.Duration
		}
	}
	p.TargetDuration = int(math.Ceil(max))
	if p.TargetDuration < 1 {
		p.TargetDuration = 1
	}
	return p
}

// TotalDuration sums the EXTINF values of all segments.
func (p *Playlist) TotalDuration() float64 {
	t := 0.0
	for _, s := range p.Segments {
		t += s.Duration
	}
	return t
}
