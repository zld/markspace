package model

// Sample is one compressed media sample. Payload bytes are opaque to the
// pipeline: remuxing must preserve them byte-for-byte.
type Sample struct {
	// Dts is the (possibly wrapped) decode timestamp in track ticks.
	Dts uint64
	// Duration in track ticks; variable frame rates are kept as-is.
	Duration uint32
	// Payload holds the complete compressed sample bytes.
	Payload []byte
	// Sync flags a random-access sample.
	Sync bool
}

// Codec identifies a compression format. Unknown values travel through the
// whole pipeline untouched but are flagged as unmappable by track mapping.
type Codec string

const (
	CodecAVC1   Codec = "avc1"
	CodecMP4A   Codec = "mp4a"
	CodecUnknown Codec = "unknown"
)

// Track is one demuxed elementary stream.
type Track struct {
	ID         uint32
	Codec      Codec
	Timescale  uint32
	Samples    []Sample
	// ClockPackets mirrors the sample timestamps as RTP packets so clock
	// recovery can run on pion/rtp timestamp semantics for every container.
	ClockPackets [][]byte
	// NotMappable marks unknown codecs: samples stay lossless but the track
	// cannot participate in explicit output mapping.
	NotMappable bool
	// DemuxErrors holds non-fatal problems found while demuxing this track
	// (e.g. continuity counter errors); other tracks remain unaffected.
	DemuxErrors []*MediaError
}

// SegmentRef is one referenced media fragment with its declared time range.
type SegmentRef struct {
	URL      string
	Index    int
	Line     int    // manifest/playlist line where the reference appears
	StartMs  int64
	DurationMs int64
}

// Representation is one DASH adaptation set member or HLS variant stream.
type Representation struct {
	ID         string
	Format     string // "dash" or "hls"
	Timescale  uint32
	Segments   []SegmentRef
	// ManifestErrors holds manifest-level problems attributed to this item;
	// they never abort processing of sibling representations.
	ManifestErrors []*MediaError
}

// Boundary marks one recovered segment boundary on the shared timeline.
type Boundary struct {
	SegmentIndex int
	StartMs      int64
	EndMs        int64
}

// AnomalyInterval is a boundary cross-check failure with every representation
// that participated in the mismatch.
type AnomalyInterval struct {
	StartMs         int64
	EndMs           int64
	ThresholdMs     int64
	MaxDeltaMs      int64
	Participants    []string
	Kind            ErrorKind
	Detail          string
}

// RecoveredTrack is the per-representation clock-recovery output. Original
// sample timestamps remain available alongside the unwrapped timeline.
type RecoveredTrack struct {
	Track       Track
	RawTimes    []uint64 // original DTS per sample, never modified
	Unwrapped   []int64  // expanded tick timestamps
	DriftPPM    int64    // estimated clock drift in parts per million
	WrapCount   int      // number of wraparounds expanded
	VFR         bool     // variable sample durations detected
	Boundaries  []Boundary
	Errors      []*MediaError
	Trusted     bool // false while any error makes boundaries untrusted
}

// RecoveredRepresentation groups the recovered tracks of one representation.
type RecoveredRepresentation struct {
	ID       string
	Format   string
	Tracks   []RecoveredTrack
	Errors   []*MediaError
	OK       bool
}
