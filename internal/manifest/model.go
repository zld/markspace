// Package manifest assembles the distribution manifest: RTP packet list,
// timeline, bitrate accounting, and gap records. It is the only place packet
// timestamps are produced, and it derives them from the shared timeline.
package manifest

import (
	"fmt"

	"mediacore/internal/audio"
	"mediacore/internal/core"
	"mediacore/internal/timeline"
)

// Config is fixed for a run. Invalid combinations are rejected before any
// packet is built.
type Config struct {
	File        string
	TargetKbps  int
	SampleRate  int
	Channels    int
	PacketMs    int
	PayloadType uint8
	SSRC        uint32
}

func (c Config) validate(layout audio.Layout) error {
	switch {
	case c.TargetKbps <= 0:
		return core.New(core.CodeParamConflict, core.StageManifest, false, "target bitrate must be positive")
	case c.SampleRate <= 0:
		return core.New(core.CodeParamConflict, core.StageManifest, false, "sample rate must be positive")
	case c.Channels <= 0 || layout.NumChannels() != c.Channels:
		return core.New(core.CodeParamConflict, core.StageManifest, false,
			fmt.Sprintf("channel count %d does not match layout %s", c.Channels, layout.Name))
	case c.PacketMs <= 0 || c.PacketMs > 1000:
		return core.New(core.CodeParamConflict, core.StageManifest, false, "packet duration out of range")
	}
	return nil
}

// Segment is one contiguous run of processed audio. StartTicks is its first
// frame on the shared output tick grid. GapsBefore counts gaps that ended
// immediately before this segment, so packet timestamps keep the hole.
type Segment struct {
	Audio         *audio.Buffer
	StartTicks    timeline.Ticks
	GapsBefore    int
	Discontinuity bool
}

// Gap is a manifest-visible hole in the output timeline.
type Gap struct {
	StartMs  int64  `json:"start_ms"`
	LengthMs int64  `json:"length_ms"`
	Reason   string `json:"reason"`
	Offset   int64  `json:"offset"`
}

// Packet is one RTP packet's auditable facts. The payload itself is not kept
// in the manifest; its length is.
type Packet struct {
	Sequence  uint16 `json:"seq"`
	Timestamp uint32 `json:"ts"`
	StartMs   int64  `json:"start_ms"`
	Samples   int    `json:"samples"`
	Bytes     int    `json:"bytes"`
	Wrapped   bool   `json:"wrapped,omitempty"`
}

// Stats mirrors audio.Stats in the manifest.
type Stats struct {
	InputFrames    int     `json:"input_frames"`
	OutputFrames   int     `json:"output_frames"`
	ClippedSamples int     `json:"clipped_samples"`
	SilentSamples  int     `json:"silent_samples"`
	Resets         int     `json:"resets"`
	TailFrames     int     `json:"tail_frames"`
	Segments       int     `json:"segments"`
	PeakDBFS       float64 `json:"peak_dbfs"`
	RMSDBFS        float64 `json:"rms_dbfs"`
}

// Manifest is the JSON document written to disk.
type Manifest struct {
	Version         int      `json:"version"`
	File            string   `json:"file"`
	SampleRate      int      `json:"sample_rate"`
	Channels        int      `json:"channels"`
	Layout          string   `json:"layout"`
	TargetKbps      int      `json:"target_kbps"`
	ActualKbps      float64  `json:"actual_kbps"`
	BitrateErrorPct float64  `json:"bitrate_error_pct"`
	PayloadBytes    int64    `json:"payload_bytes"`
	PacketCount     int      `json:"packet_count"`
	ValidMs         int64    `json:"valid_ms"`
	TotalMs         int64    `json:"total_ms"`
	InputLUFS       float64  `json:"input_lufs"`
	OutputLUFS      float64  `json:"output_lufs"`
	GainDB          float64  `json:"gain_db"`
	GainLimited     bool     `json:"gain_limited"`
	ClippedSamples  int      `json:"clipped_samples"`
	AlignmentMs     float64  `json:"alignment_ms"`
	Gaps            []Gap    `json:"gaps"`
	Packets         []Packet `json:"packets"`
	Stats           Stats    `json:"stats"`
	StageErrors     []string `json:"stage_errors,omitempty"`
	Summary         string   `json:"summary"`
}
