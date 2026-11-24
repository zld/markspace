// Package normalize resolves conflicting rotation, crop and colour claims
// using fixed, documented priority rules. It never silently overwrites a
// declaration: every applied value and every unresolved conflict is recorded.
package normalize

import (
	"fmt"
	"sort"

	"archive/internal/clock"
	"archive/internal/probe"
)

// Rule identifies which priority rule produced a resolved value.
type Rule string

const (
	RuleExplicitCrop  Rule = "explicit_crop_over_default"
	RuleDefaultFull   Rule = "default_full_frame"
	RuleTrackRotation Rule = "explicit_rotation_per_track"
	RuleColorSingle   Rule = "single_colour_declaration"
	RuleUnresolved    Rule = "unresolved"
)

// Resolution is the outcome of normalising one track's claims.
type Resolution struct {
	TrackID string

	StoredWidth  int
	StoredHeight int

	Crop       probe.CropDecl
	CropRule   Rule
	CropSource string

	Rotation     int
	RotationRule Rule

	Color         *probe.ColorDecl
	ColorResolved bool
	ColorRule     Rule
	ColorSources  []string
	ColorConflict bool

	Warnings []string
}

// OutputWidth and OutputHeight are the rotated output dimensions.
func (r Resolution) OutputWidth() int {
	if r.Rotation == 90 || r.Rotation == 270 {
		return r.Crop.Height()
	}
	return r.Crop.Width()
}

// OutputHeight is the rotated output height.
func (r Resolution) OutputHeight() int {
	if r.Rotation == 90 || r.Rotation == 270 {
		return r.Crop.Width()
	}
	return r.Crop.Height()
}

// Resolve applies the fixed conflict rules to one track.
//
//  1. A crop covering the whole stored frame is the default and is replaced by
//     any legal explicit crop. Among explicit crops, the last valid one in
//     declaration order wins (MP4 clean aperture is authoritative over MXF
//     descriptor extras because it is emitted later in track order); ties are
//     reported as warnings.
//  2. Invalid crops (empty, negative edges, or extending past the stored
//     frame) are rejected with a classified error naming the track; the track
//     produces no frames.
//  3. Rotation is taken from the track's explicit declaration. Contradictory
//     rotation declarations are reported as warnings and the first explicit
//     non-zero value wins.
//  4. Colour declarations are compared. A single distinct colour is resolved.
//     Two or more distinct colours are unresolved: the primary is kept for
//     provenance but ColorResolved is false and every source is recorded.
func Resolve(t *probe.Track) (*Resolution, error) {
	if t.StoredWidth <= 0 || t.StoredHeight <= 0 {
		return nil, clock.Newf(clock.KindInvalidMetadata,
			"stored dimensions %dx%d are not positive", t.StoredWidth, t.StoredHeight).
			WithTrack(t.ID)
	}
	r := &Resolution{
		TrackID:      t.ID,
		StoredWidth:  t.StoredWidth,
		StoredHeight: t.StoredHeight,
	}
	if err := r.resolveCrop(t); err != nil {
		return nil, err
	}
	r.resolveRotation(t)
	r.resolveColor(t)
	return r, nil
}

func (r *Resolution) resolveCrop(t *probe.Track) error {
	full := probe.CropDecl{Left: 0, Right: t.StoredWidth, Top: 0, Bottom: t.StoredHeight, Source: "default"}
	var explicit []probe.CropDecl
	for _, c := range t.Crops {
		if err := validateCrop(c, t.StoredWidth, t.StoredHeight); err != nil {
			return err.WithTrack(t.ID)
		}
		if isFullFrame(c, t.StoredWidth, t.StoredHeight) {
			r.Warnings = append(r.Warnings, fmt.Sprintf("crop from %s covers the full frame and is treated as default", c.Source))
			continue
		}
		explicit = append(explicit, c)
	}
	if len(explicit) == 0 {
		r.Crop = full
		r.CropRule = RuleDefaultFull
		r.CropSource = "default"
		return nil
	}
	// Stable order: keep declaration order, but prefer the last claim.
	chosen := explicit[len(explicit)-1]
	if len(explicit) > 1 {
		for _, c := range explicit[:len(explicit)-1] {
			if c == chosen {
				continue
			}
			r.Warnings = append(r.Warnings, fmt.Sprintf("crop conflict: %s [%d,%d,%d,%d] superseded by %s [%d,%d,%d,%d]",
				c.Source, c.Left, c.Right, c.Top, c.Bottom,
				chosen.Source, chosen.Left, chosen.Right, chosen.Top, chosen.Bottom))
		}
	}
	r.Crop = chosen
	r.CropRule = RuleExplicitCrop
	r.CropSource = chosen.Source
	return nil
}

func validateCrop(c probe.CropDecl, w, h int) *clock.Error {
	if c.Left < 0 || c.Top < 0 {
		return clock.Newf(clock.KindOutOfRange, "crop origin (%d,%d) is negative", c.Left, c.Top)
	}
	if c.Right > w || c.Bottom > h {
		return clock.Newf(clock.KindOutOfRange,
			"crop [%d,%d,%d,%d] exceeds stored frame %dx%d", c.Left, c.Right, c.Top, c.Bottom, w, h)
	}
	if c.Empty() {
		return clock.Newf(clock.KindOutOfRange, "crop [%d,%d,%d,%d] is empty", c.Left, c.Right, c.Top, c.Bottom)
	}
	return nil
}

func isFullFrame(c probe.CropDecl, w, h int) bool {
	return c.Left == 0 && c.Top == 0 && c.Right == w && c.Bottom == h
}

func (r *Resolution) resolveRotation(t *probe.Track) {
	if len(t.Rotations) == 0 {
		r.RotationRule = RuleTrackRotation
		r.Warnings = append(r.Warnings, "no rotation declared, defaulting to 0")
		return
	}
	r.RotationRule = RuleTrackRotation
	r.Rotation = t.Rotations[0].Degrees
	for _, rot := range t.Rotations[1:] {
		if rot.Degrees != r.Rotation {
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"rotation conflict: %s=%d superseded by earlier %s=%d",
				rot.Source, rot.Degrees, t.Rotations[0].Source, r.Rotation))
		}
	}
}

func (r *Resolution) resolveColor(t *probe.Track) {
	if len(t.Colors) == 0 {
		r.ColorRule = RuleUnresolved
		r.Warnings = append(r.Warnings, "no colour declaration")
		return
	}
	seen := make(map[string]probe.ColorDecl)
	for _, c := range t.Colors {
		key := fmt.Sprintf("%d/%d/%d/%t", c.Primaries, c.Transfer, c.Matrix, c.FullRange)
		seen[key] = c
		r.ColorSources = append(r.ColorSources, c.Source)
	}
	if len(seen) == 1 {
		first := t.Colors[0]
		r.Color = &first
		r.ColorResolved = true
		r.ColorRule = RuleColorSingle
		return
	}
	// Multiple distinct claims: mark unresolved, keep primary for provenance.
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	first := t.Colors[0]
	r.Color = &first
	r.ColorResolved = false
	r.ColorConflict = true
	r.ColorRule = RuleUnresolved
	r.Warnings = append(r.Warnings, fmt.Sprintf(
		"colour conflict unresolved across %d declarations (%v); primary %d/%d/%d/%t retained for provenance",
		len(seen), r.ColorSources, first.Primaries, first.Transfer, first.Matrix, first.FullRange))
}

// ApplyAll resolves every track, returning the resolutions in input order.
func ApplyAll(tracks []*probe.Track) ([]*Resolution, error) {
	out := make([]*Resolution, 0, len(tracks))
	for _, t := range tracks {
		r, err := Resolve(t)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
