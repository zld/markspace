// Package container reads generated MXF and MP4 metadata and emits a
// common track description on the master clock. Parsers only report
// structure; unknown codecs are never claimed to be decoded.
package container

// ColorDesc is an nclx-style colour description. Source identifies which
// container supplied it so arbitration conflicts stay auditable.
type ColorDesc struct {
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	FullRange bool
	Source    string // "mxf" | "mp4"
}

// TrackDesc is the normalized description of one track. All time fields
// are already expressed in master ticks.
type TrackDesc struct {
	ID            uint32
	Kind          string // "video" | "audio"
	TimescaleHz   int64  // source clock as found in the container
	FrameRateNum  int64  // rational frame rate, audio uses sample rate/1
	FrameRateDen  int64
	SampleCount   int
	DurationTicks int64
	Width         uint32
	Height        uint32
	Rotation      int
	Color         *ColorDesc
	// KeyframeSampleNumbers are 1-based sample numbers from the index.
	// Nil means no keyframe index was available.
	KeyframeSampleNumbers []uint32
}

// FileMeta is everything one container contributes to the pipeline.
type FileMeta struct {
	Format string // "mxf" | "mp4"
	Tracks []TrackDesc
	// ConflictCandidates lists fields whose raw values differ across
	// containers, already converted to comparable units.
	Color    *ColorDesc
	Rotation int
	HasIndex bool
}
