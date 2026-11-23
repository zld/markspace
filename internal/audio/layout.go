package audio

import "fmt"

// Layout names supported by the normalizer.
const (
	LayoutMono   = "mono"
	LayoutStereo = "stereo"
)

// Layout describes the PCM channel arrangement.
type Layout struct {
	Name     string
	Channels int
}

// ParseLayout maps a channel count to a supported layout.
func ParseLayout(channels int) (Layout, error) {
	switch channels {
	case 1:
		return Layout{Name: LayoutMono, Channels: 1}, nil
	case 2:
		return Layout{Name: LayoutStereo, Channels: 2}, nil
	default:
		return Layout{}, fmt.Errorf("audio: unsupported channel count %d (want 1 or 2)", channels)
	}
}

// Normalize converts interleaved input with inChannels into the target layout.
// Mono to stereo duplicates the channel with a -3 dB per-channel gain so the
// summed power is preserved; stereo to mono averages both channels (also
// power preserving). Same-layout input is copied.
func Normalize(in []float64, inChannels int, target Layout) ([]float64, error) {
	if inChannels != 1 && inChannels != 2 {
		return nil, fmt.Errorf("audio: unsupported input layout %d", inChannels)
	}
	if target.Channels != 1 && target.Channels != 2 {
		return nil, fmt.Errorf("audio: unsupported target layout %d", target.Channels)
	}
	frames := len(in) / inChannels
	out := make([]float64, frames*target.Channels)
	switch {
	case inChannels == 1 && target.Channels == 1:
		copy(out, in)
	case inChannels == 2 && target.Channels == 2:
		copy(out, in)
	case inChannels == 1 && target.Channels == 2:
		const gain = 0.7071067811865476 // -3 dB
		for i := 0; i < frames; i++ {
			v := in[i] * gain
			out[2*i], out[2*i+1] = v, v
		}
	case inChannels == 2 && target.Channels == 1:
		const gain = 0.5
		for i := 0; i < frames; i++ {
			out[i] = (in[2*i] + in[2*i+1]) * gain
		}
	}
	return out, nil
}

// ChannelCount guesses the interleaving width from a declared layout name.
func ChannelCount(name string) int {
	switch name {
	case LayoutMono:
		return 1
	case LayoutStereo:
		return 2
	default:
		return 0
	}
}
