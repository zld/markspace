package media

// Sample is one compact media sample. Data is populated only while streaming
// and is excluded from JSON indexes.
type Sample struct {
	Number        uint32 `json:"number"`
	TrackID       uint32 `json:"track_id"`
	DTS           int64  `json:"dts"`
	PTS           int64  `json:"pts"`
	Duration      uint32 `json:"duration"`
	Size          uint32 `json:"size"`
	Sync          bool   `json:"sync"`
	ChunkNumber   uint32 `json:"chunk_number"`
	ChunkOffset   int64  `json:"chunk_offset"`
	OffsetInChunk int64  `json:"offset_in_chunk"`
	SHA256        string `json:"sha256,omitempty"`
	Data          []byte `json:"-"`
}

// Segment is a deterministic group of samples, normally bounded by chunk bytes.
type Segment struct {
	Index       int      `json:"index"`
	TrackID     uint32   `json:"track_id"`
	FirstSample uint32   `json:"first_sample"`
	LastSample  uint32   `json:"last_sample"`
	Bytes       int      `json:"bytes"`
	SHA256      string   `json:"sha256"`
	SampleSHA   []string `json:"sample_sha256,omitempty"`
}

// TrackInfo records the time base, edit list, keyframes, and sample metadata.
type TrackInfo struct {
	TrackID        uint32          `json:"track_id"`
	Handler        string          `json:"handler,omitempty"`
	Timebase       Timebase        `json:"timebase"`
	MovieTimebase  Timebase        `json:"movie_timebase"`
	StartTimeTicks int64           `json:"start_time_ticks"`
	DurationTicks  uint64          `json:"duration_ticks"`
	MediaDuration  uint64          `json:"media_duration"`
	EditList       []EditListEntry `json:"edit_list,omitempty"`
	Keyframes      []Keyframe      `json:"keyframes,omitempty"`
	SampleCount    uint32          `json:"sample_count"`
	Samples        []Sample        `json:"samples,omitempty"`
	Segments       []Segment       `json:"segments,omitempty"`
	TrackSHA256    string          `json:"track_sha256,omitempty"`
	// SampleDescription is the raw stsd payload (full-box header, count, and
	// entries), retained so rewrap need not reconstruct codec configuration.
	SampleDescription []byte `json:"-"`
}

// ContainerInfo is the parsed description of one MP4/MOV source.
type ContainerInfo struct {
	Path          string      `json:"path"`
	FileSize      int64       `json:"file_size"`
	SourceSHA256  string      `json:"source_sha256"`
	MajorBrand    string      `json:"major_brand"`
	MinorVersion  uint32      `json:"minor_version"`
	MovieTimebase Timebase    `json:"movie_timebase"`
	MovieDuration uint64      `json:"movie_duration"`
	FastStart     bool        `json:"fast_start"`
	Tracks        []TrackInfo `json:"tracks"`
	Report        Report      `json:"report"`
}
