// Package audio: PCM formats, channel layouts and buffers.
package audio

import (
	"math"

	"example.com/assetpack/internal/fault"
)

// Layout names a channel layout.  The order of channels is the canonical
// order used by every buffer in this package.
type Layout string

const (
	LayoutMono   Layout = "mono"
	LayoutStereo Layout = "stereo"
	Layout5_1    Layout = "5.1"
)

// Channels returns the speaker count and the canonical channel order.
func (l Layout) Channels() ([]string, error) {
	switch l {
	case LayoutMono:
		return []string{"M"}, nil
	case LayoutStereo:
		return []string{"L", "R"}, nil
	case Layout5_1:
		return []string{"L", "R", "C", "LFE", "Ls", "Rs"}, nil
	default:
		return nil, fault.New(fault.StageAudio, fault.KindInvalidArg, "unknown channel layout %q", string(l))
	}
}

// Count is Channels without the error, for validated layouts.
func (l Layout) Count() int {
	ch, err := l.Channels()
	if err != nil {
		return 0
	}
	return len(ch)
}

// ParseLayout accepts the layout names used on the command line.
func ParseLayout(s string) (Layout, error) {
	switch Layout(s) {
	case LayoutMono, LayoutStereo, Layout5_1:
		return Layout(s), nil
	}
	return "", fault.New(fault.StageAudio, fault.KindInvalidArg,
		"unknown target layout %q (want mono, stereo or 5.1)", s)
}

// Format is a complete PCM format description.
type Format struct {
	Rate   int    `json:"rate"`
	Layout Layout `json:"layout"`
}

// Validate enforces the rate and layout ranges accepted by the pipeline.
func (f Format) Validate() error {
	if f.Rate < 8000 || f.Rate > 192000 {
		return fault.New(fault.StageAudio, fault.KindInvalidArg, "sample rate %d outside 8000..192000", f.Rate)
	}
	if _, err := f.Layout.Channels(); err != nil {
		return err
	}
	return nil
}

// PCM is deinterleaved audio: one slice per channel, all the same length.
type PCM struct {
	Format   Format
	Channels [][]float64
}

// Frames is the number of sample frames per channel.
func (p PCM) Frames() int {
	if len(p.Channels) == 0 {
		return 0
	}
	return len(p.Channels[0])
}

// Validate checks structural invariants.
func (p PCM) Validate() error {
	if err := p.Format.Validate(); err != nil {
		return err
	}
	want := p.Format.Layout.Count()
	if len(p.Channels) != want {
		return fault.New(fault.StageAudio, fault.KindInvalidArg,
			"buffer has %d channels, layout %s needs %d", len(p.Channels), p.Format.Layout, want)
	}
	n := p.Frames()
	for i, ch := range p.Channels {
		if len(ch) != n {
			return fault.New(fault.StageAudio, fault.KindInvalidArg,
				"channel %d has %d frames, channel 0 has %d", i, len(ch), n)
		}
	}
	return nil
}

// Peak returns the largest absolute sample value across channels.
func (p PCM) Peak() float64 {
	peak := 0.0
	for _, ch := range p.Channels {
		for _, v := range ch {
			if a := math.Abs(v); a > peak {
				peak = a
			}
		}
	}
	return peak
}

// Silence builds a zeroed buffer.
func Silence(f Format, frames int) PCM {
	ch := make([][]float64, f.Layout.Count())
	for i := range ch {
		ch[i] = make([]float64, frames)
	}
	return PCM{Format: f, Channels: ch}
}

// Gap is an interval of missing source material, expressed in output-clock
// sample frames, that is padded with silence.
type Gap struct {
	Start  int64 `json:"start_sample"`
	Length int64 `json:"length_samples"`
	Offset int64 `json:"source_offset"`
}
