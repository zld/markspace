// Package color normalizes frame color metadata toward an explicit target
// label. Unknown labels are never guessed: they mark the frame and track as
// unprocessable, and the engine blocks every stage depending on that track.
package color

import (
	"fmt"
	"sort"

	"mediapipe/internal/model"
)

// Profile is the full description of a recognized color label.
type Profile struct {
	Label     string
	Primaries string
	Transfer  string
	Matrix    string
}

// catalog is the closed set of labels this pipeline knows how to convert.
// Adding a label here is an explicit, reviewed decision rather than a guess.
var catalog = map[string]Profile{
	"bt601":   {Label: "bt601", Primaries: "bt601", Transfer: "bt601", Matrix: "smpte170m"},
	"bt709":   {Label: "bt709", Primaries: "bt709", Transfer: "bt709", Matrix: "bt709"},
	"bt2020":  {Label: "bt2020", Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020ncl"},
	"srgb":    {Label: "srgb", Primaries: "bt709", Transfer: "iec61966-2-1", Matrix: "rgb"},
	"unknown": {}, // explicit sentinel: handled as unprocessable, not a guess
}

// Known reports whether label is a convertible profile.
func Known(label string) bool {
	p, ok := catalog[label]
	return ok && p.Label != ""
}

// FrameResult is the normalization outcome for one frame.
type FrameResult struct {
	Index   int
	Before  model.ColorMeta
	After   model.ColorMeta
	Changed bool
}

// TrackResult aggregates per-frame results and blocks on the first unknown.
type TrackResult struct {
	TrackID      string
	Target       string
	Frames       []FrameResult
	Blocked      bool
	FirstUnknown *model.MediaError
}

// Normalize rewrites every frame's color metadata toward target, preserving
// the original label in the Source field as provenance. An unknown label
// (including the explicit "unknown" sentinel and empty tags on video) stops
// normalization and marks the track unprocessable.
func Normalize(trackID string, frames []model.Frame, target string) (*TrackResult, error) {
	tp, ok := catalog[target]
	if !ok || tp.Label == "" {
		return nil, model.NewError(model.ErrInvalidConfig, trackID, 0, 0,
			"unknown target color label "+target)
	}
	res := &TrackResult{TrackID: trackID, Target: target}
	for i, f := range frames {
		src := f.Color
		if !Known(src.Label) {
			res.Blocked = true
			res.FirstUnknown = model.NewError(model.ErrUnknownColor, trackID, i, 0,
				"label="+src.Label+" source="+src.Source)
			return res, res.FirstUnknown
		}
		after := model.ColorMeta{
			Label:     tp.Label,
			Source:    "normalized:" + src.Label + "|" + src.Source,
			Primaries: tp.Primaries,
			Transfer:  tp.Transfer,
			Matrix:    tp.Matrix,
		}
		res.Frames = append(res.Frames, FrameResult{
			Index: i, Before: src, After: after, Changed: src.Label != target,
		})
	}
	return res, nil
}

// KnownLabels returns the sorted accepted labels (excluding the sentinel).
func KnownLabels() []string {
	out := make([]string, 0, len(catalog)-1)
	for label, p := range catalog {
		if p.Label != "" {
			out = append(out, label)
		}
	}
	sort.Strings(out)
	return out
}

// String renders a profile for parameter summaries.
func (p Profile) String() string {
	return fmt.Sprintf("%s/%s/%s/%s", p.Label, p.Primaries, p.Transfer, p.Matrix)
}
