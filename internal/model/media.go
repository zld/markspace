package model

// TrackKind identifies the media carried by a track or RTP payload type.
type TrackKind string

const (
	TrackVideo TrackKind = "video"
	TrackAudio TrackKind = "audio"
)

// Frame is one access unit on the unified nanosecond timeline. PTS is the
// absolute playout time; Dur its presentation duration. KeyFrame is only
// meaningful for video. Truncated marks frames whose payload was cut short;
// silence detection must never treat such frames as evidence of non-silence.
type Frame struct {
	Kind      TrackKind `json:"kind"`
	PTS       PlayTime  `json:"pts_ns"`
	Dur       PlayTime  `json:"dur_ns"`
	KeyFrame  bool      `json:"keyframe"`
	Truncated bool      `json:"truncated"`
	Payload   []byte    `json:"-"`
}

// SourceRef records where a parsed media object came from. File is relative
// to the case root; Offset/Frame point inside it when known.
type SourceRef struct {
	Manifest       string `json:"manifest,omitempty"`
	Representation string `json:"representation,omitempty"`
	Entry          int    `json:"entry"`
	File           string `json:"file"`
	Offset         int64  `json:"offset"`
	Frame          int    `json:"frame"`
}

// MediaTrack is one parsed representation or RTP payload type.
type MediaTrack struct {
	ID        string    `json:"id"`
	Kind      TrackKind `json:"kind"`
	TimeScale TimeScale `json:"timescale"`
	ClockRate uint32    `json:"clock_rate,omitempty"` // RTP only
	Frames    []Frame   `json:"frames"`
	Source    SourceRef `json:"source"`
}

// SegmentRef is one playlist/MPD entry before media parsing.
type SegmentRef struct {
	Index   int      `json:"index"`
	URL     string   `json:"url"`
	StartTS TimeScale `json:"-"` // manifest time-scale ticks
	Start   PlayTime `json:"start_ns"`
	DurationTS int64  `json:"-"`
	Duration PlayTime `json:"duration_ns"`
	Discontinuity bool  `json:"discontinuity,omitempty"`
}

// Representation is one DASH adaptation-set entry or HLS variant rendition.
type Representation struct {
	ID       string       `json:"id"`
	Kind     TrackKind    `json:"kind"`
	TimeScale TimeScale  `json:"timescale"`
	Segments []SegmentRef `json:"segments"`
}

// Manifest is the normalized output of both DASH and HLS parsers.
type Manifest struct {
	Kind            string           `json:"kind"` // "dash" | "hls"
	Path            string           `json:"path"`
	TimeScale       TimeScale        `json:"timescale"`
	Duration        PlayTime         `json:"duration_ns"`
	Representations []Representation `json:"representations"`
	// Misaligned records representation pairs whose offset exceeds 20 ms.
	// Such pairs are never merged during deduplication.
	Misaligned []Misalignment `json:"misaligned,omitempty"`
}

// Misalignment describes a prohibited merge between two representations.
type Misalignment struct {
	RepA    string   `json:"rep_a"`
	RepB    string   `json:"rep_b"`
	OffsetNS PlayTime `json:"offset_ns"`
}
