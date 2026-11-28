// Package ingest parses the project-defined MP4 and MXF metadata subsets.
// Every retained field keeps its original value and a SourceRef (file offset
// plus structural field path); parse failures are returned as model.FieldError
// so truncation and out-of-range lengths stay locatable.
package ingest

import "mediapack/internal/model"

// TrackKind distinguishes video and audio tracks.
type TrackKind string

const (
	TrackVideo TrackKind = "video"
	TrackAudio TrackKind = "audio"
)

// MaxDimension is the inclusive per-axis input size limit.
const MaxDimension = 4096

// Crop mirrors an ISO BMFF clap box in its native rational form.
type Crop struct {
	CleanWidthN  uint32
	CleanWidthD  uint32
	CleanHeightN uint32
	CleanHeightD uint32
	HorizOffN    int32
	HorizOffD    int32
	VertOffN     int32
	VertOffD     int32
}

// EffectiveRect returns the integer clean aperture (x, y, w, h) applied to a
// frame of frameW x frameH. Fractional results floor toward zero.
func (c *Crop) EffectiveRect(frameW, frameH uint32) (x, y, w, h int64) {
	cw := int64(c.CleanWidthN) / int64(maxu32(c.CleanWidthD, 1))
	ch := int64(c.CleanHeightN) / int64(maxu32(c.CleanHeightD, 1))
	ho := int64(c.HorizOffN) / int64(maxi32(c.HorizOffD, 1))
	vo := int64(c.VertOffN) / int64(maxi32(c.VertOffD, 1))
	w = cw
	h = ch
	x = (int64(frameW)-cw)/2 + ho
	y = (int64(frameH)-ch)/2 + vo
	return x, y, w, h
}

func maxu32(v, floor uint32) uint32 {
	if v == 0 {
		return floor
	}
	return v
}

func maxi32(v, floor int32) int32 {
	if v == 0 {
		return floor
	}
	return v
}

// ColorDesc retains one color description exactly as stored.
type ColorDesc struct {
	Type      string          `json:"type"`
	Primaries uint16          `json:"primaries"`
	Transfer  uint16          `json:"transfer"`
	Matrix    uint16          `json:"matrix"`
	FullRange bool            `json:"full_range"`
	Source    model.SourceRef `json:"source"`
}

// Sample is one timeline entry: index and delta in track timescale units.
type Sample struct {
	Index int    `json:"index"`
	Delta uint32 `json:"delta"`
}

// Track is the normalized representation of one parsed track. Video-only and
// audio-only fields coexist; the Kind selects which group applies.
type Track struct {
	Index       int       `json:"index"`
	Kind        TrackKind `json:"kind"`
	TrackID     uint32    `json:"track_id"`
	Timescale   uint32    `json:"timescale"`
	DurationSrc uint64    `json:"duration_src"`
	Handler     string    `json:"handler"`

	// Video descriptor.
	Width  uint16 `json:"width,omitempty"`
	Height uint16 `json:"height,omitempty"`
	// Matrix is the raw tkhd transformation; Rotation is its derived
	// multiple of 90 degrees.
	Matrix   [9]int32 `json:"matrix,omitempty"`
	Rotation int      `json:"rotation_degrees"`
	// Crop is nil when no legal clean aperture exists: the full frame wins.
	Crop *Crop `json:"crop,omitempty"`
	// Colors holds every description found. When descriptions disagree the
	// track keeps all of them and sets ColorUnresolved instead of overwriting.
	Colors          []ColorDesc `json:"colors,omitempty"`
	ColorUnresolved bool        `json:"color_unresolved"`

	// Audio descriptor.
	Channels   uint16 `json:"channels,omitempty"`
	SampleRate uint32 `json:"sample_rate,omitempty"`
	SampleSize uint16 `json:"sample_size,omitempty"`

	// Samples carries the per-sample deltas (stts / MXF equivalent).
	Samples []Sample `json:"samples,omitempty"`

	// Sources maps field names to their provenance.
	Sources map[string]model.SourceRef `json:"sources"`
}

// Source returns the provenance recorded for a field, or a zero ref.
func (t *Track) Source(field string) model.SourceRef {
	if t.Sources == nil {
		return model.SourceRef{}
	}
	return t.Sources[field]
}

// Container is the parse output for one input file.
type Container struct {
	Format   string   `json:"format"`
	FileName string   `json:"file_name"`
	Tracks   []*Track `json:"tracks"`
	// Warnings are non-fatal structured findings (fallbacks, conflicts).
	Warnings []*model.FieldError `json:"warnings,omitempty"`
}

// VideoTracks returns tracks of kind video, in file order.
func (c *Container) VideoTracks() []*Track {
	return c.tracksOf(TrackVideo)
}

// AudioTracks returns tracks of kind audio, in file order.
func (c *Container) AudioTracks() []*Track {
	return c.tracksOf(TrackAudio)
}

func (c *Container) tracksOf(k TrackKind) []*Track {
	var out []*Track
	for _, t := range c.Tracks {
		if t.Kind == k {
			out = append(out, t)
		}
	}
	return out
}
