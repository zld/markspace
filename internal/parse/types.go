package parse

import (
	"fmt"

	"replayproof/internal/media"
)

// Kind is the role of a track in the replay program.
type Kind uint8

const (
	KindVideo Kind = iota + 1
	KindAudio
)

func (k Kind) String() string {
	switch k {
	case KindVideo:
		return "video"
	case KindAudio:
		return "audio"
	default:
		return "unknown"
	}
}

// Sample is one compressed media sample located in the source file. Times are
// exact rationals; Offset is the absolute byte position of the payload.
type Sample struct {
	Number   int
	DTS      media.Rational
	Duration media.Rational
	PTS      media.Rational
	Offset   int64
	Size     uint32
	Keyframe bool
}

// End returns the exclusive end of the sample on the decode timeline.
func (s Sample) End() media.Rational { return s.DTS.Add(s.Duration) }

// Track is a parsed track and its sample map.
type Track struct {
	ID           uint32
	Kind         Kind
	Timescale    uint32
	Codec        string
	Samples      []Sample
	Width        uint16
	Height       uint16
	ChannelCount uint16
	SampleRate   uint32
	StsdRaw      []byte
	Meta         *Meta

	// MetaOffset is the absolute byte offset of the container's metadata
	// payload, used to report where a conflict was found.
	MetaOffset int64

	// mxfEdit and mxfCount carry container-declared timing that is not part
	// of the MP4 sample table model.
	mxfEdit  media.Rational
	mxfCount uint32
}

// Duration returns the end time of the last sample.
func (t *Track) Duration() media.Rational {
	if len(t.Samples) == 0 {
		return media.Zero
	}
	s := t.Samples[len(t.Samples)-1]
	return s.End()
}

// Asset is one parsed container.
type Asset struct {
	Source string // "mp4" or "mxf"
	Path   string
	Tracks []*Track
}

// TrackByID returns the track with the given id, or nil.
func (a *Asset) TrackByID(id uint32) *Track {
	for _, t := range a.Tracks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (a *Asset) VideoTrack() *Track {
	for _, t := range a.Tracks {
		if t.Kind == KindVideo {
			return t
		}
	}
	return nil
}

func (a *Asset) AudioTrack() *Track {
	for _, t := range a.Tracks {
		if t.Kind == KindAudio {
			return t
		}
	}
	return nil
}

// SampleCount sums samples over all tracks.
func (a *Asset) SampleCount() int {
	n := 0
	for _, t := range a.Tracks {
		n += len(t.Samples)
	}
	return n
}

// Rotation is a clockwise display rotation derived from the tkhd matrix.
type Rotation struct {
	Degrees int
}

// Crop is a symmetric crop expressed as margins in luma pixels.
type Crop struct {
	Left, Top, Right, Bottom uint32
}

// Color is an nclx-style colour description.
type Color struct {
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	FullRange bool
}

// Meta contains the display descriptors that may be present on a track.
// Nil members are absent, which is different from a zero-valued descriptor.
type Meta struct {
	Rotation *Rotation
	Crop     *Crop
	Color    *Color
}

func (m *Meta) clone() *Meta {
	if m == nil {
		return nil
	}
	out := &Meta{}
	if m.Rotation != nil {
		r := *m.Rotation
		out.Rotation = &r
	}
	if m.Crop != nil {
		c := *m.Crop
		out.Crop = &c
	}
	if m.Color != nil {
		c := *m.Color
		out.Color = &c
	}
	return out
}

// Field names used by selectors and conflict reports.
const (
	FieldRotation = "rotation"
	FieldCrop     = "crop"
	FieldColor    = "color"
)

var metaFields = []string{FieldRotation, FieldCrop, FieldColor}

// MetaSource pairs an input label with its parsed descriptors.
type MetaSource struct {
	Name string
	Meta *Meta
}

// Candidate is one source's value for a field.
type Candidate struct {
	Source string
	Value  string
}

// FieldChoice records how a field was resolved.
type FieldChoice struct {
	Field       string
	Source      string // empty when unresolved
	Value       string
	Auto        bool // true when sources agreed or only one source carried it
	HadConflict bool
}

// Conflict is an unresolved metadata disagreement.
type Conflict struct {
	Field      string
	Candidates []Candidate
	Reason     string
}

func formatRotation(r *Rotation) string { return fmt.Sprintf("%d", r.Degrees) }

func formatCrop(c *Crop) string {
	return fmt.Sprintf("%d,%d,%d,%d", c.Left, c.Top, c.Right, c.Bottom)
}

func formatColor(c *Color) string {
	full := 0
	if c.FullRange {
		full = 1
	}
	return fmt.Sprintf("%d/%d/%d/%d", c.Primaries, c.Transfer, c.Matrix, full)
}

func metaValue(m *Meta, field string) (string, bool) {
	if m == nil {
		return "", false
	}
	switch field {
	case FieldRotation:
		if m.Rotation == nil {
			return "", false
		}
		return formatRotation(m.Rotation), true
	case FieldCrop:
		if m.Crop == nil {
			return "", false
		}
		return formatCrop(m.Crop), true
	case FieldColor:
		if m.Color == nil {
			return "", false
		}
		return formatColor(m.Color), true
	}
	return "", false
}
