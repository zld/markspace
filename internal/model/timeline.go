// Package model defines the shared nanosecond timebase timeline model used by
// every parser, clock estimator and downstream stage.
package model

import "sort"

// Container identifies the source container format.
type Container string

const (
	ContainerMP4 Container = "mp4"
	ContainerMXF Container = "mxf"
	ContainerRTP Container = "rtp"
)

// Kind identifies a track media kind.
type Kind string

const (
	KindAudio Kind = "audio"
	KindVideo Kind = "video"
)

// Rotation is a counter-clockwise rotation in degrees, one of 0/90/180/270.
type Rotation struct {
	Degrees       int    `json:"degrees"`
	SourceOffset  int64  `json:"source_offset"`
	SourceBox     string `json:"source_box,omitempty"`
}

// Crop is a clean-aperture crop in source pixels.
type Crop struct {
	Left   int64 `json:"left"`
	Top    int64 `json:"top"`
	Right  int64 `json:"right"`
	Bottom int64 `json:"bottom"`
}

// ColorDecl is one color description attached to a track.
type ColorDecl struct {
	ColorType   string `json:"color_type"`
	Primaries   uint16 `json:"primaries,omitempty"`
	Transfer    uint16 `json:"transfer,omitempty"`
	Matrix      uint16 `json:"matrix,omitempty"`
	FullRange   bool   `json:"full_range,omitempty"`
	ProfileLen  int    `json:"profile_len,omitempty"`
	SourceBox   string `json:"source_box,omitempty"`
	SourceOff   int64  `json:"source_offset"`
}

// ColorInfo collects all color declarations found on one track.
type ColorInfo struct {
	Declarations []ColorDecl `json:"declarations"`
	// Conflict means the declarations cannot be deterministically reconciled.
	Conflict bool `json:"conflict"`
	// Review is set by the transform stage when the choice needs human review.
	Review bool `json:"review"`
	Chosen *ColorDecl `json:"chosen,omitempty"`
}

// Sample is one audio sample or video frame on the unified nanosecond axis.
type Sample struct {
	// DTS is the decode timestamp in nanoseconds on the track-local axis.
	DTS int64 `json:"dts_ns"`
	// PTS is the presentation timestamp in nanoseconds on the track-local axis.
	PTS int64 `json:"pts_ns"`
	// DurationNanos is the presentation duration; always non-negative.
	DurationNanos int64 `json:"duration_ns"`
	// PayloadLen is the byte length of the associated sample data.
	PayloadLen int64 `json:"payload_len"`
	// PayloadOff is the byte offset inside the source container.
	PayloadOff int64 `json:"payload_offset"`
	// Keyframe marks a random-access point (video only).
	Keyframe bool `json:"keyframe,omitempty"`
	// Pcm holds generated PCM values for audio samples (fixture content).
	Pcm []int16 `json:"-"`
	// DupPTS marks a presentation timestamp also used by another sample.
	DupPTS bool `json:"dup_pts,omitempty"`
	// Missing marks a gap before this sample detected at ingest.
	Missing bool `json:"missing,omitempty"`
}

// Track is one normalized media track.
type Track struct {
	ID         string    `json:"id"`
	Kind       Kind      `json:"kind"`
	Timescale  uint32    `json:"timescale"`
	Container  Container `json:"container"`
	SourceFile string    `json:"source_file"`
	// TrackOff is the byte offset of the track-level structure in the source.
	TrackOff int64 `json:"track_offset"`
	Width    int64 `json:"width,omitempty"`
	Height   int64 `json:"height,omitempty"`
	Rotation Rotation            `json:"rotation"`
	Crop     *Crop               `json:"crop,omitempty"`
	Color    ColorInfo           `json:"color"`
	Samples  []Sample            `json:"samples"`
	// ClockPpm is the estimated device drift in parts per million (RTP only).
	ClockPpm float64 `json:"clock_ppm,omitempty"`
	// OffsetNanos maps track-local time onto the session timeline:
	// session_ns = track_ns + OffsetNanos (after alignment).
	OffsetNanos int64 `json:"offset_nanos"`
}

// Timeline is the normalized output of the ingest + clock stages.
type Timeline struct {
	SessionID  string   `json:"session_id"`
	Tracks     []*Track `json:"tracks"`
	// Quarantined counts late/out-of-order packets refused after commit.
	Quarantined int `json:"quarantined_packets"`
	// WrapCount counts observed 32-bit RTP timestamp wraps.
	WrapCount int `json:"wrap_count"`
}

// Bounds returns the min PTS and max end timestamp across all tracks.
func (t *Timeline) Bounds() (start, end int64) {
	first := true
	for _, tr := range t.Tracks {
		for _, s := range tr.Samples {
			if first || s.PTS < start {
				start = s.PTS
			}
			if e := s.PTS + s.DurationNanos; first || e > end {
				end = e
			}
			first = false
		}
	}
	return start, end
}

// SortPresentation orders samples by (PTS, DTS), as required for VFR tracks.
func (tr *Track) SortPresentation() {
	sort.SliceStable(tr.Samples, func(i, j int) bool {
		if tr.Samples[i].PTS != tr.Samples[j].PTS {
			return tr.Samples[i].PTS < tr.Samples[j].PTS
		}
		return tr.Samples[i].DTS < tr.Samples[j].DTS
	})
}
