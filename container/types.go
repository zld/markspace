// Package container reads MP4 structure onto the shared nanosecond timeline and
// recovers readable media around truncation and broken indexes.
//
// Scanning never assumes the sample tables are trustworthy. Chunk offsets,
// sample sizes and durations are validated against the real file length; when an
// index entry lies outside the file the divergence is reported and the scanner
// keeps what was valid. When the index is missing or incomplete the scanner
// walks the top-level box chain, resynchronising on plausible box headers past a
// damaged region without ever reading beyond the input.
package container

const stream = "container"

// TrackKind classifies a track by its handler.
type TrackKind int

const (
	// TrackVideo is a video track.
	TrackVideo TrackKind = iota
	// TrackAudio is an audio track.
	TrackAudio
	// TrackSubtitle is a subtitle track.
	TrackSubtitle
	// TrackOther is any handler the kernel does not interpret.
	TrackOther
)

// String renders the kind for reports.
func (k TrackKind) String() string {
	switch k {
	case TrackVideo:
		return "video"
	case TrackAudio:
		return "audio"
	case TrackSubtitle:
		return "subtitle"
	default:
		return "other"
	}
}

// Sample is one media sample placed on the shared timeline.
type Sample struct {
	// TrackID identifies the owning track.
	TrackID uint32
	// Number is the 1-based sample number inside its track.
	Number uint32
	// Offset and Size are the byte extent in the file.
	Offset int64
	Size   uint32
	// StartNS and DurationNS place the sample on the nanosecond timeline.
	StartNS    int64
	DurationNS int64
	// Sync is true for a keyframe (or an audio sample, which is always sync).
	Sync bool
	// Recovered is true when the sample was found by boundary scanning rather
	// than through a trustworthy sample table.
	Recovered bool
}

// Track summarises one track.
type Track struct {
	ID        uint32
	Kind      TrackKind
	Timescale uint32
	Duration  int64 // nanoseconds
	Samples   []Sample
}
