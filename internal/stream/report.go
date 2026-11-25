// Package stream scans sample tables and RTP packets for readability,
// timestamp monotonicity, and keyframe intervals.
package stream

import (
	"github.com/zld/media-check/internal/clock"
	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/mp4"
)

const DefaultMaxKeyframeIntervalMS int64 = 400

type SampleReport struct {
	TrackID        uint32      `json:"track_id"`
	TrackKind      string      `json:"track_kind"`
	Index          int         `json:"index"`
	AbsoluteOffset int64       `json:"absolute_offset"`
	Size           int         `json:"size_bytes"`
	Readable       bool        `json:"readable"`
	RTPOK          bool        `json:"rtp_ok"`
	IsSync         bool        `json:"is_sync"`
	MaterialID     string      `json:"material_id,omitempty"`
	PayloadSHA256  string      `json:"payload_sha256,omitempty"`
	TimeRange      clock.Range `json:"time_range"`
	PTSMS          int64       `json:"pts_ms"`
}

type KeyframeInterval struct {
	TrackID    uint32      `json:"track_id"`
	FromIndex  int         `json:"from_index"`
	ToIndex    int         `json:"to_index"`
	FromOffset int64       `json:"from_offset"`
	ToOffset   int64       `json:"to_offset"`
	IntervalMS int64       `json:"interval_ms"`
	MaximumMS  int64       `json:"maximum_ms"`
	Exceeded   bool        `json:"exceeded"`
	TimeRange  clock.Range `json:"time_range"`
}

type TrackReport struct {
	mp4.Track
	ReadableSamples int                `json:"readable_samples"`
	TotalSamples    int                `json:"total_samples"`
	SyncIndexes     []int              `json:"sync_indexes"`
	Intervals       []KeyframeInterval `json:"keyframe_intervals"`
	MaxIntervalMS   int64              `json:"max_keyframe_interval_ms"`
	TimeRange       clock.Range        `json:"time_range"`
}

type Result struct {
	InputID        string         `json:"input_id"`
	InputSHA256    string         `json:"input_sha256"`
	Tracks         []TrackReport  `json:"tracks"`
	Samples        []SampleReport `json:"samples"`
	Issues         []*fault.Error `json:"issues"`
	FirstBadOffset int64          `json:"first_bad_offset,omitempty"`
}
