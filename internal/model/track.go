// Package model describes tracks and their normalized metadata.
package model

import "fmt"

// MediaType distinguishes video and audio tracks.
type MediaType int

const (
	MediaVideo MediaType = iota
	MediaAudio
)

func (t MediaType) String() string {
	if t == MediaVideo {
		return "video"
	}
	return "audio"
}

// ChannelLayout names a loudspeaker layout.
type ChannelLayout string

const (
	LayoutMono   ChannelLayout = "mono"
	LayoutStereo ChannelLayout = "stereo"
	Layout51     ChannelLayout = "5.1"
	Layout71     ChannelLayout = "7.1"
)

// layoutChannels maps supported layouts to their channel counts.
var layoutChannels = map[ChannelLayout]int{
	LayoutMono:   1,
	LayoutStereo: 2,
	Layout51:     6,
	Layout71:     8,
}

// Channels returns the channel count of a layout and whether the layout is
// supported.
func (l ChannelLayout) Channels() (int, bool) {
	n, ok := layoutChannels[l]
	return n, ok
}

// ColorDesc carries video color metadata.
type ColorDesc struct {
	Primaries string `json:"primaries,omitempty"`
	Transfer  string `json:"transfer,omitempty"`
	Matrix    string `json:"matrix,omitempty"`
	FullRange bool   `json:"full_range,omitempty"`
}

// TrackDesc describes one track of a fragment group.
type TrackDesc struct {
	ID         uint32        `json:"id"`
	Type       MediaType     `json:"type"`
	Timescale  uint32        `json:"timescale"`
	SampleRate uint32        `json:"sample_rate,omitempty"`
	Layout     ChannelLayout `json:"layout,omitempty"`
	Color      ColorDesc     `json:"color,omitempty"`
}

// Warning reports a metadata problem that was preserved rather than fixed.
type Warning struct {
	Track  uint32 `json:"track"`
	Field  string `json:"field"`
	Detail string `json:"detail"`
}

func (w Warning) String() string {
	return fmt.Sprintf("track=%d field=%s: %s", w.Track, w.Field, w.Detail)
}

var supportedSampleRates = map[uint32]bool{44100: true, 48000: true, 96000: true}

// colorFamilies groups mutually consistent color declarations.
var colorFamilies = map[string]string{
	"bt709":     "bt709",
	"srgb":      "bt709",
	"bt2020":    "bt2020",
	"pq":        "bt2020",
	"hlg":       "bt2020",
	"bt601":     "bt601",
	"smpte170m": "bt601",
}

// Normalize validates desc and returns structured warnings for every
// unsupported or conflicting declaration. The original declarations are
// always preserved verbatim; nothing is transcoded or rewritten.
func Normalize(desc TrackDesc) ([]Warning, error) {
	if desc.Timescale == 0 {
		return nil, fmt.Errorf("track %d: timescale must be positive", desc.ID)
	}
	var warns []Warning
	warn := func(field, detail string) {
		warns = append(warns, Warning{Track: desc.ID, Field: field, Detail: detail})
	}
	switch desc.Type {
	case MediaAudio:
		if _, ok := desc.Layout.Channels(); !ok {
			warn("layout", fmt.Sprintf("unsupported channel layout %q preserved", desc.Layout))
		}
		if !supportedSampleRates[desc.SampleRate] {
			warn("sample_rate", fmt.Sprintf("unsupported sample rate %d preserved", desc.SampleRate))
		}
	case MediaVideo:
		c := desc.Color
		fam := func(v string) (string, bool) { f, ok := colorFamilies[v]; return f, ok }
		for _, pair := range [][2]string{{c.Primaries, c.Transfer}, {c.Primaries, c.Matrix}} {
			a, b := pair[0], pair[1]
			if a == "" || b == "" {
				continue
			}
			fa, oka := fam(a)
			fb, okb := fam(b)
			if oka && okb && fa != fb {
				warn("color", fmt.Sprintf("conflicting color declaration %q vs %q preserved", a, b))
			}
		}
		for _, v := range []string{c.Primaries, c.Transfer, c.Matrix} {
			if v != "" {
				if _, ok := fam(v); !ok {
					warn("color", fmt.Sprintf("unsupported color declaration %q preserved", v))
				}
			}
		}
	}
	return warns, nil
}
