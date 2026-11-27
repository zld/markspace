// Package stream analyses the byte structure of MP4 and RTP sources. Both
// analysers report the first damaged byte offset of every broken region in
// original source coordinates, using the shared fault and timebase packages.
package stream

import (
	"archivecheck/internal/fault"
	"archivecheck/internal/timebase"
)

// Structure summarises an MP4 source for the manifest.
type Structure struct {
	Source     string       `json:"source"`
	Size       int64        `json:"size"`
	Brand      string       `json:"brand,omitempty"`
	Tracks     []Track      `json:"tracks"`
	Damaged    []fault.Span `json:"damaged,omitempty"`
	BoxCount   int          `json:"boxCount"`
	ParseError string       `json:"parseError,omitempty"`
}

// Track describes one MP4 track and its keyframe layout.
type Track struct {
	ID            uint32           `json:"id"`
	Handler       string           `json:"handler"`
	Timescale     uint32           `json:"timescale"`
	DurationNanos timebase.Nanos   `json:"durationNanos"`
	SampleCount   int              `json:"sampleCount"`
	SyncSamples   int              `json:"syncSamples"`
	Keyframes     []Keyframe       `json:"keyframes,omitempty"`
	Intervals     []timebase.Nanos `json:"intervals,omitempty"`
	Interval      timebase.Nanos   `json:"intervalNanos"`
	Damaged       []fault.Span     `json:"damaged,omitempty"`
}

// Keyframe is a sync sample position. Time is on the shared nanosecond scale;
// Offset is the absolute byte position of the sample inside the source.
type Keyframe struct {
	Number int            `json:"number"`
	Offset int64          `json:"offset"`
	Time   timebase.Nanos `json:"time"`
}

// Bad reports whether the structure has any damaged span.
func (s *Structure) Bad() bool { return len(s.Damaged) > 0 }
