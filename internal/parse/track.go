// Package parse reads the supported local container subsets (MP4 sample
// tables plus rotation/crop/colour metadata, and a structured MXF KLV
// metadata subset) into a common track model. Every structural error is
// reported as a model.Fault pinned to the raw byte offset of the
// offending structure; unknown structures stop the affected track.
package parse

import (
	"archive/internal/model"

	"github.com/pion/rtp"
)

// Sample is one decoded sample-table entry (no codec decoding is done).
type Sample struct {
	Index    uint32 `json:"index"`
	DTS      int64  `json:"dts"`      // decode time in track timescale ticks
	Duration int64  `json:"duration"` // in track timescale ticks
	Offset   int64  `json:"offset"`   // byte offset of payload inside the source file
	Size     uint32 `json:"size"`
	Keyframe bool   `json:"keyframe"`
}

// ColorInfo is one colour description (ISO/IEC 23001-8 code points).
type ColorInfo struct {
	Primaries uint16 `json:"primaries"`
	Transfer  uint16 `json:"transfer"`
	Matrix    uint16 `json:"matrix"`
}

// Crop is a crop rectangle in source pixels.
type Crop struct {
	X, Y, W, H uint32
}

// Track is the container-format-neutral parse result for one track.
type Track struct {
	Source       string      `json:"source"`
	Format       string      `json:"format"` // "mp4" or "mxf"
	TrackID      int         `json:"track_id"`
	TimeScale    int64       `json:"timescale"`
	MediaOffset  int64       `json:"media_offset"` // non-zero start, track timescale ticks
	Width        uint32      `json:"width"`
	Height       uint32      `json:"height"`
	Matrix       [9]int32    `json:"matrix"`
	Crop         *Crop       `json:"crop,omitempty"`
	Colors       []ColorInfo `json:"colors,omitempty"`
	Samples      []Sample    `json:"samples"`
	RTPTimestamp uint32      `json:"rtp_timestamp"` // 90 kHz sync clock
	HasRTP       bool        `json:"has_rtp"`
}

// ParseRTPPacket decodes a synchronisation packet carried by the
// container. The RTP timestamp anchors the track on the unified clock.
func ParseRTPPacket(raw []byte, offset int64, trackIdx int) (uint32, error) {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return 0, model.ParseFault(offset, trackIdx, "malformed RTP sync packet: %v", err)
	}
	return pkt.Timestamp, nil
}
