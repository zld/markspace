// Package media parses container metadata for the supported MXF and MP4
// profiles and normalizes it into a single track model.
//
// The parser intentionally supports a bounded profile (see mxf.go and mp4.go);
// anything outside that profile is reported as a structured error instead of
// being guessed at. Video rotation, clean aperture and colour descriptions are
// kept verbatim in Description values and resolved with a documented priority:
// the first description emitted by the container (active sample entry, first
// MXF descriptor) is authoritative for every attribute it defines; attributes
// it omits fall through to later descriptions in order. Disagreements are
// counted per attribute and surfaced in Asset.Conflicts so they end up in the
// manifest summary.
package media

import (
	"fmt"

	"github.com/zld/media-regress/internal/clock"
	"github.com/zld/media-regress/internal/error"
)

// Kind is a track kind.
type Kind string

const (
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
)

// Valid reports whether k is a supported kind.
func (k Kind) Valid() bool { return k == KindVideo || k == KindAudio }

// Colour is a colour description (ISO/IEC 14496-12 colr nclx).
type Colour struct {
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	FullRange bool
}

// String renders the description deterministically.
func (c Colour) String() string {
	return fmt.Sprintf("p=%d,t=%d,m=%d,full=%t", c.Primaries, c.Transfer, c.Matrix, c.FullRange)
}

// Crop is a clean aperture in luma pixels.
type Crop struct {
	Width       uint32
	Height      uint32
	HorizOffset int32
	VertOffset  int32
}

// String renders the crop deterministically.
func (c Crop) String() string {
	return fmt.Sprintf("%dx%d+%d+%d", c.Width, c.Height, c.HorizOffset, c.VertOffset)
}

// Aspect is a pixel aspect ratio.
type Aspect struct {
	HSpacing uint32
	VSpacing uint32
}

// String renders the aspect ratio.
func (a Aspect) String() string { return fmt.Sprintf("%d:%d", a.HSpacing, a.VSpacing) }

// Description is one raw container description of a track. Priority is the
// order in which the container exposed it; 0 is authoritative.
type Description struct {
	Priority int
	Origin   string // e.g. "stsd[0]", "mxf-descriptor[1]"
	Width    uint32
	Height   uint32
	Rotation *int
	Crop     *Crop
	Aspect   *Aspect
	Colour   *Colour
	Rate     uint32 // audio sample rate
	Channels uint16
	Layout   string
}

// VideoInfo is the normalized video description.
type VideoInfo struct {
	Width    uint32
	Height   uint32
	Rotation int
	Crop     *Crop
	Aspect   *Aspect
	Colour   *Colour
}

// AudioInfo is the normalized audio description.
type AudioInfo struct {
	SampleRate uint32
	Channels   uint16
	Layout     string
}

// Segment is one container segment in track timescale ticks.
type Segment struct {
	Index    int
	Start    int64
	Duration int64
	Digest   string
	Offset   int64 // input byte offset of the segment payload
}

// Sample is an audio sample payload read from the container.
type Sample struct {
	Index  int
	Offset int64
	Data   []byte
}

// Track is a normalized track.
type Track struct {
	ID           uint32
	Kind         Kind
	Timescale    uint32
	Duration     int64 // ticks in Timescale
	Descriptions []Description
	Video        *VideoInfo
	Audio        *AudioInfo
	Conflicts    int
	Segments     []Segment
	Samples      []Sample
	InputOffset  int64
	Source       string
}

// Asset is a parsed input file.
type Asset struct {
	Format   string // "mxf" or "mp4"
	Source   string
	Digest   string // sha256 of the input bytes, hex
	Tracks   []*Track
	Conflicts int
}

// EndMaster returns the track end in master ticks.
func (t *Track) EndMaster() (int64, error) {
	if t.Timescale == 0 {
		return 0, merr.New(merr.StageMetadata, merr.KindInconsistent, t.ID, t.InputOffset,
			"track %d has zero timescale", t.ID)
	}
	return clock.ConvertTicks(t.Duration, clock.Rate(t.Timescale), clock.Master)
}

// Normalize resolves the raw descriptions with the documented priority and
// counts per-attribute conflicts. Attributes defined by the authoritative
// description win; missing attributes fall through in priority order.
func (t *Track) Normalize() {
	descs := t.Descriptions
	// Stable priority order (containers append in priority order already).
	for i := 1; i < len(descs); i++ {
		for j := i; j > 0 && descs[j-1].Priority > descs[j].Priority; j-- {
			descs[j-1], descs[j] = descs[j], descs[j-1]
		}
	}
	t.Conflicts = 0
	switch t.Kind {
	case KindVideo:
		t.Video = normalizeVideo(descs, &t.Conflicts)
	case KindAudio:
		t.Audio = normalizeAudio(descs, &t.Conflicts)
	}
}

func normalizeVideo(descs []Description, conflicts *int) *VideoInfo {
	v := &VideoInfo{}
	var rot int
	haveRot, haveCrop, haveAspect, haveColour := false, false, false, false
	for _, d := range descs {
		if d.Width != 0 && v.Width == 0 {
			v.Width = d.Width
		}
		if d.Height != 0 && v.Height == 0 {
			v.Height = d.Height
		}
		if d.Rotation != nil {
			if !haveRot {
				rot, haveRot = *d.Rotation, true
			} else if *d.Rotation != rot {
				*conflicts++
			}
		}
		if d.Crop != nil {
			if !haveCrop {
				c := *d.Crop
				v.Crop, haveCrop = &c, true
			} else if *d.Crop != *v.Crop {
				*conflicts++
			}
		}
		if d.Aspect != nil {
			if !haveAspect {
				a := *d.Aspect
				v.Aspect, haveAspect = &a, true
			} else if *d.Aspect != *v.Aspect {
				*conflicts++
			}
		}
		if d.Colour != nil {
			if !haveColour {
				c := *d.Colour
				v.Colour, haveColour = &c, true
			} else if *d.Colour != *v.Colour {
				*conflicts++
			}
		}
	}
	v.Rotation = rot
	return v
}

func normalizeAudio(descs []Description, conflicts *int) *AudioInfo {
	a := &AudioInfo{}
	for _, d := range descs {
		if d.Rate != 0 {
			if a.SampleRate == 0 {
				a.SampleRate = d.Rate
			} else if d.Rate != a.SampleRate {
				*conflicts++
			}
		}
		if d.Channels != 0 {
			if a.Channels == 0 {
				a.Channels = d.Channels
			} else if d.Channels != a.Channels {
				*conflicts++
			}
		}
		if d.Layout != "" {
			if a.Layout == "" {
				a.Layout = d.Layout
			} else if d.Layout != a.Layout {
				*conflicts++
			}
		}
	}
	return a
}

// Validate performs structural checks after parsing.
func (t *Track) Validate() error {
	if !t.Kind.Valid() {
		return merr.New(merr.StageMetadata, merr.KindInconsistent, t.ID, t.InputOffset,
			"track %d has unsupported kind %q", t.ID, t.Kind)
	}
	if t.Timescale == 0 {
		return merr.New(merr.StageMetadata, merr.KindInconsistent, t.ID, t.InputOffset,
			"track %d has zero timescale", t.ID)
	}
	if t.Duration <= 0 {
		return merr.New(merr.StageMetadata, merr.KindInconsistent, t.ID, t.InputOffset,
			"track %d has non-positive duration %d", t.ID, t.Duration)
	}
	switch t.Kind {
	case KindVideo:
		if t.Video == nil || t.Video.Width == 0 || t.Video.Height == 0 {
			return merr.New(merr.StageMetadata, merr.KindInconsistent, t.ID, t.InputOffset,
				"video track %d is missing dimensions", t.ID)
		}
		if t.Video.Rotation%90 != 0 || t.Video.Rotation < 0 || t.Video.Rotation > 270 {
			return merr.New(merr.StageMetadata, merr.KindInconsistent, t.ID, t.InputOffset,
				"video track %d has invalid rotation %d", t.ID, t.Video.Rotation)
		}
	case KindAudio:
		if t.Audio == nil || t.Audio.SampleRate == 0 || t.Audio.Channels == 0 {
			return merr.New(merr.StageMetadata, merr.KindInconsistent, t.ID, t.InputOffset,
				"audio track %d is missing sample rate or channel count", t.ID)
		}
	}
	return nil
}
