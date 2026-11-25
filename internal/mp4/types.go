package mp4scan

import "archivecheck/internal/clock"

// Sample is a decoded sample-table entry on the shared clock.
type Sample struct {
	Index    uint32 `json:"index"`
	DTS      int64  `json:"dts_tick"`
	PTS      int64  `json:"pts_tick"`
	Size     uint32 `json:"size_bytes"`
	Keyframe bool   `json:"keyframe"`
}

// EditInfo is a parsed edit-list entry.
type EditInfo struct {
	Index             int   `json:"index"`
	SegmentDuration   int64 `json:"segment_duration_tick"`
	MediaTime         int64 `json:"media_time_tick"`
	Empty             bool  `json:"empty"`
	StartTick         int64 `json:"start_tick"`
	EndTick           int64 `json:"end_tick"`
	ManifestStartTick int64 `json:"manifest_start_tick,omitempty"`
	ManifestEndTick   int64 `json:"manifest_end_tick,omitempty"`
}

// ScanResult contains all container facts needed by planning and recovery.
type ScanResult struct {
	Path               string         `json:"path"`
	SHA256             string         `json:"sha256"`
	TrackID            uint32         `json:"track_id"`
	Timebase           clock.Timebase `json:"timebase"`
	Timeline           clock.Timeline `json:"timeline"`
	Edits              []EditInfo     `json:"edits"`
	Samples            []Sample       `json:"samples"`
	SampleDurationTick clock.Tick     `json:"sample_duration_tick"`
	Keyframes          []uint32       `json:"keyframes"`
}
