// Package fanout builds multi-bitrate representation layers on the shared
// tick clock. Each layer is packetized with pion/rtp, passed through a
// deterministic local network simulation (duplicates, reordering, loss) and
// re-assembled by a receiver that deduplicates, reorders and records missing
// sequence ranges as gaps. Missing media is never fabricated.
package fanout

import (
	"mediapack/internal/render"
)

// PayloadType is the fixed RTP payload type for the project subset.
const PayloadType uint8 = 96

// MaxChunkPayload bounds one RTP packet's custom payload so every frame is
// fragmented into several packets.
const MaxChunkPayload = 24

// LayerSpec configures one bitrate representation.
type LayerSpec struct {
	Name         string `json:"name"`
	BitrateKbps  int    `json:"bitrate_kbps"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	SSRC         uint32 `json:"ssrc"`
	FramesPerSeg int    `json:"frames_per_segment"`
}

// LayerConfig returns the render geometry of a layer.
func (l LayerSpec) LayerConfig() render.LayerConfig {
	return render.LayerConfig{
		Name: l.Name, Bitrate: l.BitrateKbps, Width: l.Width, Height: l.Height,
	}
}

// Gap is a contiguous missing RTP sequence range within one segment.
type Gap struct {
	SegmentIndex int    `json:"segment_index"`
	StartSeq     uint16 `json:"start_seq"`
	EndSeq       uint16 `json:"end_seq"` // inclusive
	Count        int    `json:"count"`
	// Recovered is set when a resume retransmission closed the gap using the
	// original packet set; it never means media was fabricated.
	Recovered bool `json:"recovered"`
}

// Segment groups consecutive frames and their re-assembled packets.
type Segment struct {
	Index      int      `json:"index"`
	Layer      string   `json:"layer"`
	TickStart  int64    `json:"tick_start"`
	TickEnd    int64    `json:"tick_end"`
	FrameStart int      `json:"frame_start"`
	FrameEnd   int      `json:"frame_end"` // inclusive
	Seqs       []uint16 `json:"seqs"`
	Gaps       []Gap    `json:"gaps"`
	Complete   bool     `json:"complete"`
}

// FrameRef identifies one frame carried inside a representation.
type FrameRef struct {
	SegmentIndex int
	FrameIndex   int
	Tick         int64
	SourceTrack  int
	SourceSample int
	Hash         string
}

// Representation is the re-assembled result for one bitrate layer.
type Representation struct {
	Layer         LayerSpec  `json:"layer"`
	Segments      []Segment  `json:"segments"`
	Frames        []FrameRef `json:"frames"`
	Sent          int        `json:"packets_sent"`
	Received      int        `json:"packets_received_unique"`
	Duplicates    int        `json:"duplicates"`
	Gaps          []Gap      `json:"gaps"`
	RecoveredGaps int        `json:"recovered_gaps"`
	Reordered     bool       `json:"reordered"`
	FirstSeq      uint16     `json:"first_seq"`
	LastSeq       uint16     `json:"last_seq"`
}

// Complete reports whether every segment assembled without gaps.
func (r *Representation) Complete() bool {
	for i := range r.Segments {
		if !r.Segments[i].Complete {
			return false
		}
	}
	return len(r.Segments) > 0
}

// LastCompletedSegment returns the highest complete segment index, or -1.
func (r *Representation) LastCompletedSegment() int {
	last := -1
	for i := range r.Segments {
		if r.Segments[i].Complete {
			last = r.Segments[i].Index
		}
	}
	return last
}

// LastFrameIn returns the last frame index belonging to segment seg, or -1.
func (r *Representation) LastFrameIn(seg int) int {
	for i := range r.Segments {
		if r.Segments[i].Index == seg {
			return r.Segments[i].FrameEnd
		}
	}
	return -1
}
