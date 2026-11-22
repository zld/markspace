package model

// Archive-wide guard rails (spec section "约束").
const (
	MaxFileBytes    = 512 * 1024 * 1024 // 512 MiB per container
	MaxTracks       = 16
	MaxSamples      = 2_000_000
	MaxBoxNesting   = 32
	MaxBoxHeaderLen = 16 // size(4)+type(4)+largesize(8)
)

// Track kinds.
const (
	KindVideo = "video"
	KindAudio = "audio"
)

// Sample is one normalized media sample with bounded absolute byte range.
type Sample struct {
	Index      uint32 // 0-based position inside the track
	DTS        uint64 // decode time in track timescale units
	Duration   uint32 // duration in track timescale units
	Offset     uint64 // absolute byte offset inside the file
	Size       uint32
	IsKeyframe bool
}

// Edit mirrors an ISO BMFF edit-list entry (elst), in track/movie timescales.
type Edit struct {
	TrackID      uint32
	SegmentDur   uint64 // movie timescale units
	MediaTime    int64  // -1 = empty edit
	MediaRate    Rat
}

// ColorMeta holds the raw color description found on a video track.
type ColorMeta struct {
	ColorType string // "nclc" or "nclx"; empty when absent
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	Present   bool
}

// ChannelLayout is the normalized description of an audio track's layout.
type ChannelLayout struct {
	Count       uint16
	Labels      []string // canonical speaker labels, len == Count
	RawTag      uint32   // container-defined layout tag, 0 if unknown
	SampleRate  uint32
	Valid       bool
}

// Track is the normalized view of one trak box.
type Track struct {
	ID         uint32
	Kind       string
	Timescale  uint32
	Duration   uint64 // mdhd duration in timescale units
	Language   string
	Handler    string
	Samples    []Sample
	Edits      []Edit
	Color      ColorMeta
	Audio      ChannelLayout
	StartDelay uint64 // first-sample media time offset (timescale units)
	IndexMode  string // "native" (stss), "rebuilt" or "none"
}

// MediaTime converts a track-scaled tick count to exact seconds.
func (t *Track) MediaTime(ticks uint64) (Rat, error) {
	return NewRat(int64(ticks), uint64(t.Timescale))
}

// Decision records a normalization choice (audio/color) without claiming the
// actual signal was transcoded.
type Decision struct {
	Scope    string // "audio:<trackID>" or "color:<trackID>"
	Field    string
	Input    string
	Output   string
	Action   string // "map", "passthrough", "warning", "conflict-resolved"
	Priority int
	Reason   string
}

// Warning is a non-fatal manifest-level observation.
type Warning struct {
	Scope   string
	Code    string
	Message string
}

// Summary is the deterministic digest of one processed asset.
type Summary struct {
	AssetName      string `json:"asset_name"`
	TrackCount     int    `json:"track_count"`
	SampleCount    int    `json:"sample_count"`
	KeyframeCount  int    `json:"keyframe_count"`
	MovieTimescale uint32 `json:"movie_timescale"`
	MovieDuration  uint64 `json:"movie_duration"`
	BoxDigest      string `json:"box_digest"`
	Decisions      []Decision
	Warnings       []Warning
}
