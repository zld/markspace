package model

// ColorSource names where a color declaration came from. The order of the
// constants is the resolution precedence used by the video stage: an explicit
// container declaration wins over a bitstream tag, which wins over defaults.
type ColorSource string

// Color declaration sources, in precedence order.
const (
	ColorFromContainer ColorSource = "mp4-colr"
	ColorFromBitstream ColorSource = "sei-colour"
	ColorFromManifest  ColorSource = "manifest"
)

// ColorDeclaration is one source's claim about the video color metadata.
// Zero values are "unspecified" per ISO/IEC 23001-8. Fields mirror the nclx
// box: colour primaries, transfer characteristics and matrix coefficients.
type ColorDeclaration struct {
	Source    ColorSource `json:"source"`
	Primaries uint16      `json:"primaries"`
	Transfer  uint16      `json:"transfer"`
	Matrix    uint16      `json:"matrix"`
	FullRange bool        `json:"full_range"`
}

// SameColor reports whether two declarations agree on every field.
func (c ColorDeclaration) SameColor(o ColorDeclaration) bool {
	return c.Primaries == o.Primaries && c.Transfer == o.Transfer &&
		c.Matrix == o.Matrix && c.FullRange == o.FullRange
}

// AudioDescriptor is one source's claim about an audio track's format.
// Layout is an optional explicit layout name such as "stereo" or "5.1".
type AudioDescriptor struct {
	Source       string `json:"source"`
	ChannelCount int    `json:"channel_count"`
	SampleRate   uint32 `json:"sample_rate"`
	Layout       string `json:"layout,omitempty"`
}
