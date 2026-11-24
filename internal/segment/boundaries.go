// Package segment selects shared keyframe boundaries and cuts deterministic
// per-representation segment records.
package segment

import (
	"github.com/zld/mediarc/internal/index"
	"github.com/zld/mediarc/internal/model"
	"github.com/zld/mediarc/internal/timeline"
)

// Boundary is a selected keyframe on one representation.
type Boundary struct {
	TimeNS       model.NS       `json:"time_ns"`
	DeviationNS  model.NS       `json:"deviation_ns"`
	TrackID      uint32         `json:"track_id"`
	SampleNumber uint32         `json:"sample_number"`
	RawTimestamp uint64         `json:"raw_timestamp"`
	Offset       int64          `json:"byte_offset"`
	Keyframe     index.Keyframe `json:"-"`
}

// RepSegment contains one representation's boundary frames and chunk hash.
type RepSegment struct {
	TrackID        uint32   `json:"track_id"`
	Representation string   `json:"representation"`
	BitrateKbps    int      `json:"bitrate_kbps,omitempty"`
	Start          Boundary `json:"start"`
	End            Boundary `json:"end"`

	StartSample uint32 `json:"start_sample"`
	EndSample   uint32 `json:"end_sample_exclusive"`
	ChunkCount  int    `json:"chunk_count"`
	ByteCount   int    `json:"byte_count"`
	SHA256      string `json:"sha256"`
	ChunkFile   string `json:"chunk_file,omitempty"`
	ChunkBytes  []byte `json:"-"`
}

// Segment is one shared program-time interval across all active renditions.
type Segment struct {
	Index            int          `json:"index"`
	StartNS          model.NS     `json:"start_ns"`
	EndNS            model.NS     `json:"end_ns"`
	DurationNS       model.NS     `json:"duration_ns"`
	TargetDurationNS model.NS     `json:"target_duration_ns"`
	Partial          bool         `json:"partial"`
	Reason           string       `json:"reason,omitempty"`
	SHA256           string       `json:"sha256"`
	Representations  []RepSegment `json:"representations"`
}

// TrackReport records explicit handling for empty or excluded tracks.
type TrackReport struct {
	TrackID        uint32 `json:"track_id"`
	Representation string `json:"representation"`
	BitrateKbps    int    `json:"bitrate_kbps,omitempty"`
	Empty          bool   `json:"empty"`
	Included       bool   `json:"included"`
	Reason         string `json:"reason,omitempty"`
}

// Report is the cutting result.
type Report struct {
	ReferenceTrackID uint32        `json:"reference_track_id"`
	Segments         []Segment     `json:"segments"`
	Tracks           []TrackReport `json:"tracks"`
	Issues           []model.Issue `json:"issues,omitempty"`
	KeyframeHits     int           `json:"keyframe_hits"`
	KeyframeChecks   int           `json:"keyframe_checks"`
}

type cutContext struct {
	built   *timeline.Built
	index   *index.Index
	cfg     model.Config
	tracks  []model.TrackMetadata
	payload func(trackID, sampleNumber uint32) []byte
}

func boundaryFromKeyframe(kf index.Keyframe, dev model.NS) Boundary {
	return Boundary{
		TimeNS:       kf.TimeNS,
		DeviationNS:  dev,
		TrackID:      kf.TrackID,
		SampleNumber: kf.SampleNumber,
		RawTimestamp: kf.RawTimestamp,
		Offset:       kf.Offset,
		Keyframe:     kf,
	}
}
