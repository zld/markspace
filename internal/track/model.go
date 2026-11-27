// Package track holds the container-independent track description produced by
// the MP4 and MXF parsers, plus the dependency graph that orders processing
// stages.
package track

import (
	"fmt"
	"sort"

	"mediaregress/internal/media"
)

// ParseVersion identifies the metadata interpretation rules. It is emitted
// with every provenance record so a manifest can be tied to parser behaviour.
const ParseVersion = "trackparse/1"

// Kind is the essence kind of a track.
type Kind string

const (
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
)

// Source names the exact container construct a value came from. Raw values are
// kept verbatim next to decoded values; provenance never rewrites the source.
type Source struct {
	Container string `json:"container"` // "mp4" or "mxf"
	Element   string `json:"element"`   // box path or KLV set/UL fragment
	Offset    int64  `json:"offset"`    // input byte offset of the construct
}

// Sample is one container sample (a video access unit or an audio packet).
// Times are already on the shared nanosecond scale.
type Sample struct {
	Index    int         `json:"index"`
	Offset   int64       `json:"offset"`
	Size     uint32      `json:"size"`
	Start    media.Ticks `json:"start_ns"`
	Duration media.Ticks `json:"duration_ns"`
	Keyframe bool        `json:"keyframe"`
}

// End is the exclusive end of the sample.
func (s Sample) End() media.Ticks { return s.Start + s.Duration }

// Aperture is a clean aperture / crop rectangle in encoded pixels.
type Aperture struct {
	Left   int64  `json:"left"`
	Top    int64  `json:"top"`
	Width  int64  `json:"width"`
	Height int64  `json:"height"`
	Source Source `json:"source"`
}

// Validate rejects rectangles that cannot describe any picture. The caller
// turns a failure into a rejected track and keeps the first bad offset.
func (a Aperture) Validate(frameWidth, frameHeight int64) error {
	if a.Width <= 0 || a.Height <= 0 {
		return fmt.Errorf("crop width=%d height=%d must be positive", a.Width, a.Height)
	}
	if a.Left < 0 || a.Top < 0 {
		return fmt.Errorf("crop origin (%d,%d) must be non-negative", a.Left, a.Top)
	}
	if frameWidth > 0 && a.Left+a.Width > frameWidth {
		return fmt.Errorf("crop right %d exceeds frame width %d", a.Left+a.Width, frameWidth)
	}
	if frameHeight > 0 && a.Top+a.Height > frameHeight {
		return fmt.Errorf("crop bottom %d exceeds frame height %d", a.Top+a.Height, frameHeight)
	}
	return nil
}

// ColorDescription captures one color labelling as written in the container.
// Raw strings preserve the exact numbers; the decoded fields are convenience.
type ColorDescription struct {
	Primaries         uint16 `json:"primaries"`
	Transfer          uint16 `json:"transfer"`
	Matrix            uint16 `json:"matrix"`
	FullRange         bool   `json:"full_range"`
	HasFullRange      bool   `json:"has_full_range"`
	Raw               string `json:"raw"`
	Profile           []byte `json:"profile,omitempty"`
	Source            Source `json:"source"`
	Interpretation    string `json:"interpretation"` // e.g. "nclx" or "icc"
	RequiresRendering bool   `json:"requires_rendering"`
}

// ColorValue is one side of a conflict, kept with its origin.
type ColorValue struct {
	Field  string `json:"field"`
	Value  string `json:"value"`
	Source Source `json:"source"`
}

// ColorConflict is recorded when a track carries mutually inconsistent color
// descriptions. The parser never picks a winner; downstream steps see the
// conflict and the original descriptions.
type ColorConflict struct {
	Field  string       `json:"field"`
	Values []ColorValue `json:"values"`
}

// Track is the normalized description of one container track. Rejected is set
// for tracks whose metadata cannot be used; Errors explains why and remains
// part of the manifest.
type Track struct {
	ID              string             `json:"id"`
	Container       string             `json:"container"`
	Kind            Kind               `json:"kind"`
	Timescale       uint32             `json:"timescale"`
	Language        string             `json:"language,omitempty"`
	Duration        media.Ticks        `json:"duration_ns"`
	FrameWidth      int64              `json:"frame_width,omitempty"`
	FrameHeight     int64              `json:"frame_height,omitempty"`
	EditOffset      media.Ticks        `json:"edit_offset_ns"`
	Rotation        int                `json:"rotation_deg"`
	RotMatrix       [9]int32           `json:"rotation_matrix"`
	RotSource       Source             `json:"rotation_source"`
	Crop            *Aperture          `json:"crop,omitempty"`
	Colors          []ColorDescription `json:"colors,omitempty"`
	Conflicts       []ColorConflict    `json:"color_conflicts,omitempty"`
	Samples         []Sample           `json:"samples,omitempty"`
	Truncated       bool               `json:"truncated,omitempty"`
	TruncatedAt     media.Ticks        `json:"truncated_at_ns,omitempty"`
	TruncatedOffset int64              `json:"truncated_offset,omitempty"`
	SourceStart     int64              `json:"source_start"`
	Errors          []*media.Error     `json:"errors,omitempty"`
	Rejected        bool               `json:"rejected,omitempty"`
	Version         string             `json:"parse_version"`
}

// AddError attaches a structured error and marks the track unusable when the
// class is fatal for metadata interpretation or content reads.
func (t *Track) AddError(err *media.Error) {
	if err == nil {
		return
	}
	if err.Track == "" {
		err = err.WithTrack(t.ID)
	}
	t.Errors = append(t.Errors, err)
	// Truncation and corrupt bytes are diagnostics: whatever was readable
	// before the damage stays usable, and downstream stages are told where the
	// usable region ends. Only metadata that makes interpretation impossible
	// rejects the track.
	switch err.Class {
	case media.ClassInvalidCrop, media.ClassInvalidTimebase,
		media.ClassTimeRegression, media.ClassOutOfRange:
		t.Rejected = true
	}
}

// FirstOffset returns the earliest positioned error in the track.
func (t *Track) FirstOffset() (int64, *media.Error, bool) {
	return media.FirstOffset(t.Errors)
}

// DurationOrEnd returns the larger of the declared duration and the last
// sample end, which keeps truncated-but-usable tails visible.
func (t *Track) DurationOrEnd() media.Ticks {
	end := t.Duration
	for _, s := range t.Samples {
		if e := s.End(); e > end {
			end = e
		}
	}
	return end
}

// SampleAt returns the sample whose half-open span contains t.
func (t *Track) SampleAt(at media.Ticks) (Sample, bool) {
	i := sort.Search(len(t.Samples), func(i int) bool { return t.Samples[i].End() > at })
	if i < len(t.Samples) && t.Samples[i].Start <= at {
		return t.Samples[i], true
	}
	return Sample{}, false
}

// Source is a json-friendly view of the track's originating file.
type Input struct {
	Path   string         `json:"path"`
	Size   int64          `json:"size"`
	Digest string         `json:"sha256"`
	Tracks []*Track       `json:"tracks"`
	Errors []*media.Error `json:"errors,omitempty"`
}

// ValidTracks returns the tracks usable for downstream stages, in ID order.
func (in *Input) ValidTracks() []*Track {
	out := make([]*Track, 0, len(in.Tracks))
	for _, t := range in.Tracks {
		if !t.Rejected {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
