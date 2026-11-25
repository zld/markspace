package wav

import (
	"continuity/internal/media"
)

// Layout describes the normalized multichannel layout of a PCM stream.
// Channel order follows the WAV interleaving convention and is part of
// the content digest inputs, so two streams with identical bytes but
// different declared layouts never hash equal.
type Layout struct {
	Channels   uint16
	BitDepth   uint16
	SampleRate uint32
	// Order is the canonical channel name per slot, e.g.
	// ["L","R"] for stereo. The strings are part of the digest input.
	Order []string
}

// knownOrders maps channel count to the canonical channel order used by
// the project's generated samples.
var knownOrders = map[uint16][]string{
	1: {"C"},
	2: {"L", "R"},
	6: {"L", "R", "C", "LFE", "BL", "BR"},
}

// NewLayout builds and validates a layout for the given format fields.
func NewLayout(channels uint16, bitDepth uint16, sampleRate uint32) (Layout, error) {
	order, ok := knownOrders[channels]
	if !ok {
		return Layout{}, media.WrapError(media.CatLayout, "wav: no canonical order for channel count", nil)
	}
	l := Layout{Channels: channels, BitDepth: bitDepth, SampleRate: sampleRate, Order: order}
	return l, l.Validate()
}

// Validate checks internal consistency of the layout.
func (l Layout) Validate() error {
	if l.Channels == 0 {
		return media.NewError(media.CatLayout, "layout: zero channels")
	}
	if l.SampleRate == 0 {
		return media.NewError(media.CatLayout, "layout: zero sample rate")
	}
	switch l.BitDepth {
	case 8, 16, 24:
	default:
		return media.NewError(media.CatLayout, "layout: unsupported bit depth")
	}
	if len(l.Order) != int(l.Channels) {
		return media.NewError(media.CatLayout, "layout: channel order length mismatch")
	}
	return nil
}

// FrameBytes returns the byte size of one interleaved frame.
func (l Layout) FrameBytes() int {
	return int(l.Channels) * int(l.BitDepth) / 8
}

// SampleBytes returns the byte size of one sample in one channel.
func (l Layout) SampleBytes() int { return int(l.BitDepth) / 8 }

// Matches reports whether two layouts are identical.
func (l Layout) Matches(other Layout) bool {
	if l.Channels != other.Channels || l.BitDepth != other.BitDepth || l.SampleRate != other.SampleRate {
		return false
	}
	for i := range l.Order {
		if l.Order[i] != other.Order[i] {
			return false
		}
	}
	return true
}

// LayoutFor derives the canonical layout for a parsed WAV file.
func LayoutFor(f *File) (Layout, error) {
	return NewLayout(f.Channels, f.BitsPerSample, f.SampleRate)
}
