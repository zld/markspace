// Package meta parses MXF and MP4 track metadata (rotation, crop rectangle
// and colour descriptors), resolves conflicting fields between containers and
// reports every unresolved or malformed input as a structured model error.
package meta

import (
	"mediad/internal/clock"
	"mediad/internal/model"
)

// Source identifies where a metadata value came from. Precedence on conflict
// is MXF > MP4 > default, matching the kernel's arbitration rule.
type Source string

const (
	SourceMXF     Source = "mxf"
	SourceMP4     Source = "mp4"
	SourceDefault Source = "default"
)

// Default colour descriptor when neither container carries one. BT.709 is the
// web/HD default and therefore the kernel's explicit fallback.
const (
	DefaultColorPrimaries = "bt709"
	DefaultColorTransfer  = "bt709"
	DefaultColorMatrix    = "bt709"
)

// Color describes a three-part colour descriptor.
type Color struct {
	Primaries string `json:"primaries"`
	Transfer  string `json:"transfer"`
	Matrix    string `json:"matrix"`
	Source    Source `json:"source"`
}

// Crop is a crop rectangle in source pixels, plus its provenance.
type Crop struct {
	X      uint32 `json:"x"`
	Y      uint32 `json:"y"`
	Width  uint32 `json:"width"`
	Height uint32 `json:"height"`
	Source Source `json:"source"`
}

// FullFrame is the explicit default crop: no pixels removed.
func FullFrame(width, height uint32) Crop {
	return Crop{Width: width, Height: height, Source: SourceDefault}
}

// Conflict records one field for which MXF and MP4 disagreed, including both
// raw values and the source of the winner, so the summary can attribute it.
type Conflict struct {
	Field  string            `json:"field"`
	Chosen Source            `json:"chosen"`
	Values map[Source]string `json:"values"`
}

// TrackMeta is one parsed, arbitrated track.
type TrackMeta struct {
	TrackNo  int        `json:"track_no"`
	Kind     string     `json:"kind"` // "video" | "audio"
	Clock    clock.Rate `json:"clock"`
	Width    uint32     `json:"width"`
	Height   uint32     `json:"height"`
	Rotation int        `json:"rotation_deg"`
	RotSrc   Source     `json:"rotation_source"`
	Crop     Crop       `json:"crop"`
	Color    Color      `json:"color"`
	// Conflicts is empty when the two containers agreed on every field.
	Conflicts []Conflict `json:"conflicts,omitempty"`
}

// Result bundles successfully parsed tracks with structured parse errors.
// Independent tracks survive a broken neighbour: callers process Tracks and
// surface Errors side by side instead of aborting the whole stream.
type Result struct {
	Tracks []*TrackMeta          `json:"tracks"`
	Errors []*model.MediaError   `json:"errors,omitempty"`
}

// ConflictCount is a convenience for the metrics layer.
func (r *Result) ConflictCount() int {
	n := 0
	for _, t := range r.Tracks {
		n += len(t.Conflicts)
	}
	return n
}

// ErrorCodes returns the distinct error codes observed during parsing.
func (r *Result) ErrorCodes() []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range r.Errors {
		if !seen[string(e.Code)] {
			seen[string(e.Code)] = true
			out = append(out, string(e.Code))
		}
	}
	return out
}
