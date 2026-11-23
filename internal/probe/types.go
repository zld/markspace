// Package probe scans MXF and ISO BMFF/MP4 containers and reports track
// metadata plus structured, byte-offset-accurate errors. It never decodes
// media samples: only box/KLV structure and declared metadata are inspected.
package probe

import "mediapack/internal/model"

// Limits bounds every scan so hostile inputs cannot exhaust memory.
type Limits struct {
	MaxInputBytes   int64
	MaxTracks       int
	MaxBoxes        int
	MaxBoxDepth     int
	MaxMetaPerTrack int
}

// DefaultLimits are conservative bounds suitable for the demo pipeline.
func DefaultLimits() Limits {
	return Limits{
		MaxInputBytes:   1 << 30, // 1 GiB
		MaxTracks:       64,
		MaxBoxes:        1 << 20,
		MaxBoxDepth:     32,
		MaxMetaPerTrack: 256,
	}
}

// Kind enumerates the high-level track handlers we understand.
type Kind string

const (
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
	KindData  Kind = "data"
)

// SourcedValue keeps a normalized value next to the raw declaration and the
// byte interval of the box/tag it came from, so conflicts stay auditable.
type SourcedValue struct {
	Source string      `json:"source"` // box type / KLV local tag name
	Offset int64       `json:"offset"` // absolute byte offset in input
	Raw    string      `json:"raw"`    // original declaration, verbatim text
	Value  interface{} `json:"value"`  // normalized value
}

// ColorInfo holds the raw, possibly conflicting color declarations. The
// normalized choice and its winning source are resolved by NormalizeColor.
type ColorInfo struct {
	Primaries  []SourcedValue `json:"primaries,omitempty"`
	Transfer   []SourcedValue `json:"transfer,omitempty"`
	Matrix     []SourcedValue `json:"matrix,omitempty"`
	ICCProfile []SourcedValue `json:"icc_profile,omitempty"`
}

// TrackMeta is the normalized summary of one track plus every raw value that
// contributed to it. All time fields are integer ticks in TimeScale units.
type TrackMeta struct {
	ID            uint32        `json:"id"`
	Kind          Kind          `json:"kind"`
	Handler       string        `json:"handler"`
	TimeScale     uint32        `json:"time_scale"`
	FirstSampleTS int64         `json:"first_sample_ts"`
	SampleCount   int64         `json:"sample_count"`
	DurationTS    int64         `json:"duration_ts"`
	Width         int64         `json:"width_encoded"`
	Height        int64         `json:"height_encoded"`
	Rotation      *SourcedValue `json:"rotation,omitempty"`
	CleanAperture *SourcedValue `json:"clean_aperture,omitempty"`
	Color         ColorInfo     `json:"color"`
	// RTP anchor state for clock-drift estimation (optional).
	RTPClockRate uint32 `json:"rtp_clock_rate,omitempty"`
	RTPBaseTS    uint32 `json:"rtp_base_ts,omitempty"`
	HasRTPAnchor bool   `json:"has_rtp_anchor,omitempty"`
}

// FileMeta is the scan result for one input file.
type FileMeta struct {
	Path     string      `json:"path"`
	Format   string      `json:"format"` // "mp4" | "mxf"
	Size     int64       `json:"size"`
	SHA256   string      `json:"sha256"`
	Brand    string      `json:"brand,omitempty"`
	MXFOP    string      `json:"mxf_op,omitempty"`
	Tracks   []TrackMeta `json:"tracks"`
	BoxCount int         `json:"box_count,omitempty"`
	ScanVer  string      `json:"scan_version"`
}

// ScanVersion is bumped whenever scanning semantics change; index entries
// carrying an older version are invalidated.
const ScanVersion = "scan-3"

// ErrorRange is a small constructor for half-open byte intervals.
func ErrorRange(offset, length int64) model.ByteRange {
	return model.ByteRange{Offset: offset, Length: length}
}
