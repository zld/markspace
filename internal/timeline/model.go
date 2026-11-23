// Package timeline builds a single integer-nanosecond timeline from the
// multi-track Matroska blocks recovered by the ebml package. All time math
// stays in int64 nanoseconds so no floating-point drift can accumulate.
package timeline

import "fmt"

// TrackKind identifies the media carried by a track.
type TrackKind uint8

const (
	KindUnknown TrackKind = iota
	KindVideo
	KindAudio
	KindSubtitle
)

// MaxTracks is the hard cap on simultaneously handled tracks.
const MaxTracks = 32

// Error categories shared across the pipeline stages.
const (
	CategoryInvalidTrack   = "invalid_track_reference"
	CategoryNonMonotonic   = "non_monotonic_time"
	CategoryOutOfRange     = "sample_out_of_source_range"
	CategoryTrackLimit     = "track_limit_exceeded"
	CategoryDependency     = "unsatisfied_dependency"
	CategoryDuplicate      = "duplicate_sample"
)

// Error is a structured timeline failure carrying a stable category and the
// track/offset that triggered it.
type Error struct {
	Category    string
	TrackNumber uint32
	Offset      int64
	TimeNs      int64
	Message     string
}

func (e *Error) Error() string {
	return fmt.Sprintf("timeline: %s track=%d at %dns: %s",
		e.Category, e.TrackNumber, e.TimeNs, e.Message)
}

// Track describes one media stream and its declared dependencies.
type Track struct {
	Number           uint32
	Kind             TrackKind
	Codec            string
	DefaultDurationNs int64
	// DependsOn lists track numbers this track consumes. They must appear
	// earlier in the graph ordering.
	DependsOn []uint32
}

// Sample is one block positioned on the shared nanosecond timebase.
type Sample struct {
	TrackNumber uint32
	StartNs     int64
	DurationNs  int64
	Keyframe    bool
	// Offset is the source byte offset, kept so a damaged offset can never be
	// silently confused with a valid sample downstream.
	Offset int64
	Size   int
}

// EndNs returns the exclusive end time of the sample.
func (s Sample) EndNs() int64 { return s.StartNs + s.DurationNs }

// Clock is the shared timebase: a nanosecond origin plus the observed source
// range. Non-zero segment start times are preserved, not normalized away.
type Clock struct {
	// OriginNs is the timebase origin; samples keep their absolute StartNs.
	OriginNs int64
	MinNs    int64
	MaxNs    int64
}

// InRange reports whether t falls inside the observed source range.
func (c Clock) InRange(t int64) bool {
	return t >= c.MinNs && t <= c.MaxNs
}

// Observe extends the known source range to include t.
func (c *Clock) Observe(t int64) {
	if c.MinNs == 0 && c.MaxNs == 0 {
		c.MinNs, c.MaxNs = t, t
		return
	}
	if t < c.MinNs {
		c.MinNs = t
	}
	if t > c.MaxNs {
		c.MaxNs = t
	}
}
