// Package stream reads raw H.264/H.265 Annex B and AV1 OBU bitstreams
// together with their keyframe indexes. It never modifies timestamps: every
// emitted unit and boundary event keeps its original byte offset so later
// stages can always trace a normalized tick back to its source position.
package stream

// Codec identifies the bitstream coding format.
type Codec string

const (
	CodecH264 Codec = "h264"
	CodecH265 Codec = "h265"
	CodecAV1  Codec = "av1"
)

// Valid reports whether c is a supported codec.
func (c Codec) Valid() bool {
	switch c {
	case CodecH264, CodecH265, CodecAV1:
		return true
	default:
		return false
	}
}

// Unit is one coding unit (Annex B NAL unit or AV1 OBU) located in the
// original bitstream. Data holds the unit bytes without start-code prefix
// (Annex B) or OBU header framing overhead beyond the natural header.
type Unit struct {
	Ordinal int    `json:"ordinal"`
	Type    uint8  `json:"type"`
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	Key     bool   `json:"key"`
	Data    []byte `json:"-"`
}

// BoundaryEvent marks a keyframe cut point in the original stream. It pairs
// the byte-anchored unit with the RTP transport coordinates used by the
// source-switch stage.
type BoundaryEvent struct {
	Ordinal      int    `json:"ordinal"`
	Offset       int64  `json:"offset"`
	Length       int64  `json:"length"`
	Key          bool   `json:"key"`
	RTPTimestamp uint32 `json:"rtp_timestamp"`
	RTPSeq       uint16 `json:"rtp_seq"`
}

// IndexEntry is one keyframe record from the sidecar index file.
type IndexEntry struct {
	Offset int64 `json:"offset"`
	Length int64 `json:"length"`
}

// Index is the parsed keyframe index sidecar.
type Index struct {
	Codec   Codec        `json:"codec"`
	Entries []IndexEntry `json:"entries"`
}

// ScanResult is the output of a successful scan: all units plus the
// keyframe boundary events anchored at original byte offsets.
type ScanResult struct {
	Codec      Codec
	Units      []Unit
	Boundaries []BoundaryEvent
}
