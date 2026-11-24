package mp4

import (
	"mediad/internal/clock"
)

type RepairPolicy struct {
	// MaxGapTicks is the largest DTS hole bridgeable by continuing the local
	// cadence. Larger holes are reported as unrepairable.
	MaxGapTicks int64
	// MaxRTPGapTicks is the largest RTP timestamp jump accepted before treating
	// it as an outage rather than a wrap.
	MaxRTPGapTicks int64
	// MaxReasonableTicks bounds absurd stored timestamps.
	MaxReasonableTicks int64
}

// DefaultPolicy suits 90 kHz long-form recordings.
func DefaultPolicy() RepairPolicy {
	return RepairPolicy{
		MaxGapTicks:        20 * 90000,
		MaxRTPGapTicks:     10 * 90000,
		MaxReasonableTicks: 30 * 24 * 3600 * 90000,
	}
}

// TrackSpec is the manifest-declared time base for a track.
type TrackSpec struct {
	Timescale uint32
	OriginNs  int64
}

// RTPRecord is one decoded sidecar packet.
type RTPRecord struct {
	Timestamp uint32
	CaptureNs int64
	SSRC      uint32
}

// RepairOptions ties scanned media to manifest metadata and the network feed.
type RepairOptions struct {
	Policy            RepairPolicy
	RTPRecords        []RTPRecord
	TrackSpecs        map[uint32]TrackSpec
	RecordingOriginNs int64
}

// RTPAnchor records the wrap analysis for provenance.
type RTPAnchor struct {
	FirstTimestamp uint32 `json:"first_timestamp"`
	LastTimestamp  uint32 `json:"last_timestamp"`
	ExtendedLast   uint64 `json:"extended_last"`
	WrapCount      int    `json:"wrap_count"`
	SSRC           uint32 `json:"ssrc"`
	PacketCount    int    `json:"packet_count"`
	// GapTicks is the largest single-packet timestamp step observed.
	GapTicks uint64 `json:"max_gap_ticks"`
}

// RepairedSample is one sample on the repaired timeline.
type RepairedSample struct {
	Index     uint32 `json:"index"`
	Offset    uint64 `json:"offset"`
	Size      uint32 `json:"size"`
	RawOffset int64  `json:"raw_offset"`
	DTS       int64  `json:"dts"`
	PTS       int64  `json:"pts"`
	OrigDTS   int64  `json:"orig_dts"`
	OrigPTS   int64  `json:"orig_pts"`
	NS        int64  `json:"ns"`
	Sync      bool   `json:"sync"`
	Repaired  bool   `json:"repaired"`
}

// RepairInterval documents one contiguous correction.
type RepairInterval struct {
	TrackID    uint32     `json:"track_id"`
	Cause      clock.Code `json:"cause"`
	FirstIndex uint32     `json:"first_index"`
	LastIndex  uint32     `json:"last_index"`
	RawOffset  int64      `json:"raw_offset"`
	// CumulativeShiftTicks is the total offset applied to this sample after
	// repairing all earlier regressions on the track.
	CumulativeShiftTicks int64  `json:"cumulative_shift_ticks"`
	Count                int    `json:"count"`
	Detail               string `json:"detail"`
}

// RepairedTrack is a track on the shared nanosecond timeline.
type RepairedTrack struct {
	TrackID          uint32           `json:"track_id"`
	Kind             string           `json:"kind"`
	Timescale        uint32           `json:"timescale"`
	OriginNs         int64            `json:"origin_ns"`
	EditList         clock.EditList   `json:"edit_list"`
	RawDurationTicks int64            `json:"raw_duration_ticks"`
	Samples          []RepairedSample `json:"samples"`
	Intervals        []RepairInterval `json:"repair_intervals"`
	RepairCount      int              `json:"repair_count"`
	FirstNs          int64            `json:"first_ns"`
	LastNs           int64            `json:"last_ns"`
	RTP              *RTPAnchor       `json:"rtp,omitempty"`
	SyncFromFlags    bool             `json:"sync_from_flags"`
}

// Repaired is the full repair result for one file.
type Repaired struct {
	// Path is excluded from delivery digests; identical bytes in another
	// directory must hash identically.
	Path       string          `json:"-"`
	MovieScale uint32          `json:"movie_timescale"`
	Tracks     []RepairedTrack `json:"tracks"`
	Issues     []Problem       `json:"issues,omitempty"`
}

// Repair applies policy to every track of a scan.
