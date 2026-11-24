// Package demux parses MP4/fMP4, MPEG-TS and length-framed RTP streams into a
// common in-memory media model.  Every parse failure is reported as a
// fault.Error carrying the stage and the raw input byte offset of the first
// byte that could not be parsed.
package demux

import (
	"mediapipeline/internal/clock"
	"mediapipeline/internal/metadata"
	"mediapipeline/internal/track"
)

// Sample is one coded media sample.  Data aliases the caller's input slice.
type Sample struct {
	Offset int64
	Size   uint32
	// Delta is the sample duration in track timescale ticks.
	Delta uint32
	DTS   clock.Time
	CTS   clock.Time
	// Sync marks a random-access sample.  Samples are sync unless an stss
	// table says otherwise.
	Sync bool
	Data []byte
}

// Track is a demuxed elementary track.  ID is the identifier used by the
// container (track_ID for MP4, PID for TS); it is preserved by the mapping.
type Track struct {
	ID        uint32
	Kind      track.Kind
	Timescale uint32
	Codec     string
	Handler   string
	// Stsd is the complete stsd box (header included) copied verbatim so a
	// remux never re-interprets codec private data.
	Stsd []byte
	// NALLengthSize is the AVC/HEVC NAL length prefix width in bytes, or 0 for
	// non-video tracks.
	NALLengthSize uint8
	Samples       []Sample
}

// Media is the result of demuxing one input.
type Media struct {
	Format     string
	MajorBrand string
	Tracks     []Track
	Events     []metadata.Event
	// Cut records input ranges that were cut off and could not be parsed.
	Cut []Range
}

// Options controls optional demux behaviour.
type Options struct {
	// RequireIndex rejects fragmented MP4 streams without a segment index.
	RequireIndex bool
	// TolerateTruncated allows a truncated top-level box at the end of the
	// input.  Complete data before it is still returned and the cut range is
	// recorded.
	TolerateTruncated bool
	// SampleEvents controls derivation of events from media samples
	// (subtitles, chapters, ID3 samples and SEI NAL units).
	SampleEvents bool
	// AllowMissingIndex permits fragmented MP4 without mfra/sidx.  The normal
	// parse stage leaves it false so the absence is reported; the recovery
	// scan sets it true because it rebuilds the index itself.
	AllowMissingIndex bool
}

// Range is a half-open byte range in the original input.
type Range struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Segment is one complete moof/mdat fragment recovered by sequential scan.
type Segment struct {
	ID         int      `json:"id"`
	MoofOffset int64    `json:"moof_offset"`
	MoofSize   int64    `json:"moof_size"`
	DataOffset int64    `json:"data_offset"`
	Size       int64    `json:"size"`
	Tracks     []uint32 `json:"tracks"`
	Payload    []byte   `json:"-"`
	// src aliases the complete input so a recovered segment can be re-parsed
	// with its original absolute offsets.
	src []byte
}

// FragmentScan is the result of scanning a fragmented MP4 without relying on
// an index box.
type FragmentScan struct {
	Format         string
	IndexMissingAt int64
	Segments       []Segment
	Incomplete     []Range
	TrackIDs       []uint32
}
