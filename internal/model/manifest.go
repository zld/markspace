// Package model defines the shared manifest model: input encoding metadata,
// the normalized timeline and the in-memory MPD representation used by every
// downstream package.
package model

import "fmt"

// Default limits applied when the CLI does not override them. They keep memory
// usage bounded when fed unexpectedly large metadata files.
const (
	DefaultMaxRepresentations = 32
	DefaultMaxSegments        = 10000
)

// Segment is one encoded fragment as described by the encoding metadata.
type Segment struct {
	// Number is the $Number$ sequence index. Must be unique within a rendition.
	Number int `json:"number"`
	// StartMs is the fragment start time expressed in milliseconds relative to
	// the rendition's own media timeline.
	StartMs int64 `json:"start_ms"`
	// DurationMs is the fragment duration in milliseconds. The final fragment
	// may be shorter than the nominal target duration.
	DurationMs int64 `json:"duration_ms"`
}

// Rendition is one encoded representation (one bitrate ladder rung).
type Rendition struct {
	ID        string `json:"id"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Bandwidth int    `json:"bandwidth_bps"`
	Codec     string `json:"codec"`
	FrameRate string `json:"frame_rate"`
	// StartOffsetMs is the rendition's non-zero presentation offset relative to
	// the common period origin; zero means the rendition starts at period start.
	StartOffsetMs int64 `json:"start_offset_ms"`
	// SegmentDurationMs is the nominal target fragment duration.
	SegmentDurationMs int64 `json:"segment_duration_ms"`
	// Initialization is the initialization segment path template (may contain
	// $RepresentationID$); empty means no initialization segment is referenced.
	Initialization string `json:"initialization"`
	// Media is the media fragment path template, e.g. "video/$RepresentationID$/seg-$Number$.m4s".
	Media string `json:"media"`
	// SegmentRoot anchors every referenced path. References must stay inside it.
	SegmentRoot string    `json:"segment_root"`
	Segments    []Segment `json:"segments"`
}

// Metadata is the complete encoding result consumed by the tool.
type Metadata struct {
	// Title is informational only and never affects ordering.
	Title      string      `json:"title"`
	Renditions []Rendition `json:"renditions"`
}

// SegmentInfo is a normalized fragment expressed on the common integer timeline.
type SegmentInfo struct {
	Number   int   // sequence index carried over from the metadata
	Start    int64 // offset from period start in timescale units
	Duration int64 // duration in timescale units
}

// TimelineEntry is one run of equal consecutive durations for an S element.
type TimelineEntry struct {
	// Start is the absolute timestamp of the first fragment in the run.
	Start int64
	// Duration is the common fragment duration in timescale units.
	Duration int64
	// Repeat is the number of additional fragments in the run (r attribute).
	Repeat int
}

// RenditionManifest is one normalized representation.
type RenditionManifest struct {
	ID             string
	Width          int
	Height         int
	Bandwidth      int
	Codec          string
	FrameRate      string
	Timescale      int
	StartOffset    int64 // in timescale units
	Initialization string
	Media          string
	SegmentRoot    string
	Segments       []SegmentInfo
	Timeline       []TimelineEntry
	// Duration is the representation duration in timescale units, measured from
	// the period origin (offset + fragment durations).
	Duration int64
}

// Manifest is the normalized, cross-rendition aligned MPD model.
type Manifest struct {
	Title      string
	Timescale  int
	Renditions []*RenditionManifest // sorted by ascending bandwidth
}

// PeriodDuration returns the longest representation duration, which defines the
// common period duration in timescale units.
func (m *Manifest) PeriodDuration() int64 {
	var d int64
	for _, r := range m.Renditions {
		if r.Duration > d {
			d = r.Duration
		}
	}
	return d
}

// FindRendition returns the manifest representation with the given id.
func (m *Manifest) FindRendition(id string) (*RenditionManifest, error) {
	for _, r := range m.Renditions {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, fmt.Errorf("representation %q not found in manifest", id)
}
