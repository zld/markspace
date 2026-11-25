// Package meta normalizes rotation, crop and color descriptors coming from
// different containers. Every candidate keeps its source; when candidates
// disagree the fixed priority order adjudicates, and equal-priority conflicts
// that cannot be resolved become structured LocatableErrors.
package meta

import (
	"fmt"

	"rtpreplay/media"
)

// Field names used in traces and conflict errors.
const (
	FieldRotation = "rotation"
	FieldCrop     = "crop"
	FieldColor    = "color"
)

// Fixed priority order: larger value wins. MP4 track-level boxes are the most
// specific, MXF picture descriptors follow; lower-priority container hints
// only fill fields the higher-priority source omits.
const (
	PriorityMP4 = 2
	PriorityMXF = 1
)

// Candidate is one raw descriptor with its provenance.
type Candidate struct {
	Field    string
	Source   string // e.g. "mp4:tkhd", "mp4:colr:nclx", "mxf:cdci"
	Priority int
	Rotation *Rotation
	Crop     *Crop
	Color    *Color
}

// Rotation is a counter-clockwise rotation in quarter-turn degrees.
type Rotation struct {
	Degrees int // one of 0, 90, 180, 270
}

// Crop is the clean-aperture rectangle relative to the coded frame.
type Crop struct {
	Left, Top, Right, Bottom uint32
	Width, Height            uint32
}

// Color is a normalized color descriptor.
type Color struct {
	ColourType              string // "nclx", "rICC" or "prof"
	ColourPrimaries         uint16
	TransferCharacteristics uint16
	MatrixCoefficients      uint16
	FullRange               bool
}

// SourceTrace records how one field was adjudicated.
type SourceTrace struct {
	Field    string
	Source   string
	Priority int
	Value    string
	Chosen   bool
}

// Normalized is the adjudicated result plus the full evidence trail.
type Normalized struct {
	Rotation Rotation
	Crop     *Crop
	Color    *Color
	Traces   []SourceTrace
}

// Normalize applies the fixed-priority rules to all candidates. It returns a
// KindConflict error when equal-priority candidates disagree irreconcilably.
func Normalize(candidates []Candidate) (*Normalized, error) {
	out := &Normalized{Rotation: Rotation{Degrees: 0}}
	if err := resolveField(candidates, FieldRotation, out); err != nil {
		return nil, err
	}
	if err := resolveField(candidates, FieldCrop, out); err != nil {
		return nil, err
	}
	if err := resolveField(candidates, FieldColor, out); err != nil {
		return nil, err
	}
	return out, nil
}

func resolveField(candidates []Candidate, field string, out *Normalized) error {
	var relevant []Candidate
	for _, c := range candidates {
		if c.Field == field {
			relevant = append(relevant, c)
		}
	}
	if len(relevant) == 0 {
		return nil // missing optional metadata is not an error
	}
	// Highest priority among the candidates.
	top := relevant[0].Priority
	for _, c := range relevant {
		if c.Priority > top {
			top = c.Priority
		}
	}
	var topTier []Candidate
	for _, c := range relevant {
		if c.Priority == top {
			topTier = append(topTier, c)
		}
		out.Traces = append(out.Traces, SourceTrace{
			Field: field, Source: c.Source, Priority: c.Priority,
			Value: describe(field, c), Chosen: false,
		})
	}
	chosen, err := adjudicate(field, topTier)
	if err != nil {
		return err
	}
	for i := range out.Traces {
		if out.Traces[i].Field == field && out.Traces[i].Source == chosen.Source {
			out.Traces[i].Chosen = true
		}
	}
	apply(field, chosen, out)
	return nil
}

func adjudicate(field string, tier []Candidate) (Candidate, error) {
	winner := tier[0]
	for _, c := range tier[1:] {
		if describe(field, c) != describe(field, winner) {
			return Candidate{}, media.NewError(media.KindConflict, "METADATA_CONFLICT", "meta", -1,
				"%s: equal-priority sources %q and %q disagree (%s vs %s)",
				field, winner.Source, c.Source, describe(field, winner), describe(field, c))
		}
	}
	return winner, nil
}

func apply(field string, c Candidate, out *Normalized) {
	switch field {
	case FieldRotation:
		if c.Rotation != nil {
			out.Rotation = *c.Rotation
		}
	case FieldCrop:
		out.Crop = c.Crop
	case FieldColor:
		out.Color = c.Color
	}
}

func describe(field string, c Candidate) string {
	switch field {
	case FieldRotation:
		if c.Rotation == nil {
			return "absent"
		}
		return fmt.Sprintf("rotation=%d", c.Rotation.Degrees)
	case FieldCrop:
		if c.Crop == nil {
			return "absent"
		}
		return fmt.Sprintf("crop=(%d,%d,%d,%d) %dx%d",
			c.Crop.Left, c.Crop.Top, c.Crop.Right, c.Crop.Bottom, c.Crop.Width, c.Crop.Height)
	case FieldColor:
		if c.Color == nil {
			return "absent"
		}
		return fmt.Sprintf("color=%s cp=%d tc=%d mc=%d full=%t",
			c.Color.ColourType, c.Color.ColourPrimaries, c.Color.TransferCharacteristics,
			c.Color.MatrixCoefficients, c.Color.FullRange)
	}
	return "unknown"
}

// ValidateRotation rejects degrees outside the quarter-turn set.
func ValidateRotation(r Rotation) error {
	switch r.Degrees {
	case 0, 90, 180, 270:
		return nil
	default:
		return media.NewError(media.KindBadInput, "BAD_ROTATION", "meta", -1,
			"rotation %d is not a quarter turn", r.Degrees)
	}
}

// ValidateColor checks the nclx numeric ranges used by the sample fixtures.
func ValidateColor(c *Color) error {
	if c == nil {
		return nil
	}
	switch c.ColourType {
	case "nclx", "rICC", "prof":
	default:
		return media.NewError(media.KindBadInput, "BAD_COLOR_TYPE", "meta", -1,
			"unknown colour type %q", c.ColourType)
	}
	if c.ColourType == "nclx" {
		for name, v := range map[string]uint16{
			"primaries": c.ColourPrimaries,
			"transfer":  c.TransferCharacteristics,
			"matrix":    c.MatrixCoefficients,
		} {
			if v == 0 || v > 255 {
				return media.NewError(media.KindBadInput, "BAD_COLOR_VALUE", "meta", -1,
					"nclx %s = %d outside 1..255", name, v)
			}
		}
	}
	return nil
}

// RotationFromMatrix derives quarter-turn degrees from an MP4 3x3 matrix
// (fixed-point 16.16 entries). Unknown matrices report zero with no error.
func RotationFromMatrix(m [9]int32) Rotation {
	const one = 0x00010000
	switch {
	case m[0] == one && m[4] == one:
		return Rotation{Degrees: 0}
	case m[0] == 0 && m[4] == 0 && m[1] == one && m[3] == -one:
		return Rotation{Degrees: 90}
	case m[0] == -one && m[4] == -one:
		return Rotation{Degrees: 180}
	case m[0] == 0 && m[4] == 0 && m[1] == -one && m[3] == one:
		return Rotation{Degrees: 270}
	default:
		return Rotation{Degrees: 0}
	}
}
