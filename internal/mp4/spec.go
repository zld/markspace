package mp4

// TimelineScale is the common integer millisecond timeline timescale.
const TimelineScale uint32 = 1000

// ManifestVersion is the synthetic media-check container manifest version.
const ManifestVersion = 1

// Box type names used by the small synthetic MP4-compatible container.
const (
	BoxManifest  = "mcmf"
	BoxMediaData = "mdat"
	BoxFileType  = "ftyp"
)

// Manifest is the deterministic track/sample/timeline description carried in
// the custom mcmf box. Binary media packets remain in mdat.
type Manifest struct {
	Version       int          `json:"version"`
	Seed          int64        `json:"seed"`
	TimelineScale uint32       `json:"timeline_timescale"`
	Tracks        []Track      `json:"tracks"`
	Samples       []Sample     `json:"samples"`
	Segments      []Segment    `json:"segments"`
	Transitions   []Transition `json:"transitions"`
	Edits         []Edit       `json:"edits"`
}

type Track struct {
	TrackID       uint32 `json:"track_id"`
	Kind          string `json:"kind"` // video, audio, subtitle
	Timescale     uint32 `json:"timescale"`
	Duration      int64  `json:"duration_ticks"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
	Channels      int    `json:"channels,omitempty"`
	SampleRate    int    `json:"sample_rate,omitempty"`
	KeyframeEvery int    `json:"keyframe_every_samples,omitempty"`
}

type Sample struct {
	TrackID      uint32 `json:"track_id"`
	Index        int    `json:"index"`
	PTS          int64  `json:"pts_ticks"`
	DTS          int64  `json:"dts_ticks"`
	Duration     int64  `json:"duration_ticks"`
	IsSync       bool   `json:"is_sync"`
	Size         int    `json:"size_bytes"`
	MdatOffset   int64  `json:"mdat_offset"`
	RTPSequence  uint16 `json:"rtp_sequence"`
	RTPTimestamp uint32 `json:"rtp_timestamp"`
	MaterialID   string `json:"material_id,omitempty"`
}

// Segment is a timeline clip. Start/End use Manifest.TimelineScale.
type Segment struct {
	ID               string `json:"id"`
	TrackID          uint32 `json:"track_id"`
	MaterialID       string `json:"material_id"`
	TimelineStart    int64  `json:"timeline_start_ms"`
	TimelineEnd      int64  `json:"timeline_end_ms"`
	FirstSampleIndex int    `json:"first_sample_index"`
	LastSampleIndex  int    `json:"last_sample_index"`
}

type Transition struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	FromSegmentID string `json:"from_segment_id"`
	ToSegmentID   string `json:"to_segment_id"`
	StartMS       int64  `json:"start_ms"`
	EndMS         int64  `json:"end_ms"`
}

type Edit struct {
	ID      string `json:"id"`
	TrackID uint32 `json:"track_id"`
	Kind    string `json:"kind"`
	AtMS    int64  `json:"at_ms"`
}
