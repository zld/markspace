// Package model holds the shared data structures describing renditions,
// timestamps, cut segments and the on-disk index used across the pipeline.
package model

import "time"

// PacketSize is the fixed MPEG-2 transport stream packet size in bytes.
const PacketSize = 188

// SyncByte marks the start of every transport stream packet.
const SyncByte byte = 0x47

// Rendition describes one bitrate variant of a source transport stream.
type Rendition struct {
	// Bps is the target video bitrate in bits per second.
	Bps int `json:"rendition_bps"`
	// Source is the absolute or working-relative path to the source .ts file.
	Source string `json:"source"`
	// Dir is the output directory name holding this rendition's segments.
	Dir string `json:"dir"`
}

// Segment is the canonical record of one HLS segment, stored in index.json
// and used to regenerate the expected playlist during validation.
type Segment struct {
	// Seq is the zero-based media sequence number.
	Seq int `json:"seq"`
	// File is the basename of the segment file (never a path with separators).
	File string `json:"file"`
	// StartPTS is the unwrapped 90 kHz presentation timestamp of the first
	// frame in the segment.
	StartPTS int64 `json:"start_pts"`
	// EndPTS is the unwrapped PTS just past the last frame in the segment.
	EndPTS int64 `json:"end_pts"`
	// DurationMS is the playback duration in whole milliseconds. It is
	// persisted so a validator reloading the index keeps duration metrics.
	DurationMSField int64 `json:"duration_ms"`
	// Bytes is the segment file size at slice time.
	Bytes int64 `json:"bytes"`
}

// Duration derives the playback duration from the PTS span.
func (s Segment) Duration() time.Duration {
	return FromTicks(s.EndPTS - s.StartPTS)
}

// DurationMS returns the segment duration in whole milliseconds, preferring
// the explicitly persisted value and falling back to the PTS-derived one.
func (s Segment) DurationMS() int64 {
	if s.DurationMSField != 0 {
		return s.DurationMSField
	}
	return s.Duration().Milliseconds()
}

// RenditionResult is the per-rendition summary printed by demo and validate.
type RenditionResult struct {
	Bps          int       `json:"rendition_bps"`
	SegmentCount int       `json:"segment_count"`
	DurationMS   int64     `json:"duration_ms"`
	DeviationMS  int64     `json:"deviation_ms"`
	FailureCount int       `json:"failure_count"`
	Failures     []Finding `json:"failures,omitempty"`
}

// Finding is a single, script-parseable validation or parse problem.
type Finding struct {
	// Code is a stable machine readable failure code, e.g. MISSING_SEGMENT.
	Code string `json:"code"`
	// Bps is the rendition bitrate the finding refers to, 0 when global.
	Bps int `json:"bps,omitempty"`
	// Seq is the segment sequence number when the finding is segment scoped.
	Seq int `json:"seq,omitempty"`
	// Detail is a human readable explanation with concrete offsets/values.
	Detail string `json:"detail"`
}

// FrameTolerance is one 25 fps frame expressed in 90 kHz PTS ticks.
const FrameTolerance int64 = 3600

// ClockRate is the PTS/DTS clock frequency in hertz.
const ClockRate int64 = 90000

// Ticks converts a duration to 90 kHz clock ticks.
func Ticks(d time.Duration) int64 {
	return int64(d) * ClockRate / int64(time.Second)
}

// FromTicks converts 90 kHz clock ticks to a duration.
func FromTicks(ticks int64) time.Duration {
	return time.Duration(ticks) * time.Second / time.Duration(ClockRate)
}
