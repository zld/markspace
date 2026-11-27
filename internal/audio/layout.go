// Package audio holds the fixed-order processing chain:
// resample, channel layout normalization, then loudness control.
package audio

import (
	merr "medialib/internal/errors"
)

// PCM is interleaved float32 audio at a fixed rate and channel count.
type PCM struct {
	Rate     int
	Channels int
	Samples  []float32
}

// Frames returns the number of sample frames.
func (p PCM) Frames() int {
	if p.Channels == 0 {
		return 0
	}
	return len(p.Samples) / p.Channels
}

// MapChannels normalizes the channel layout to outChannels (1 or 2
// supported as targets): mono is replicated, anything wider is
// averaged down, stereo is kept or averaged to mono.
func MapChannels(in PCM, outChannels int) (PCM, error) {
	if outChannels != 1 && outChannels != 2 {
		return PCM{}, merr.Newf(merr.StageLayout, -1, "map",
			"unsupported target layout %d", outChannels)
	}
	if in.Channels <= 0 || in.Rate <= 0 {
		return PCM{}, merr.Newf(merr.StageLayout, -1, "map",
			"invalid input layout ch=%d rate=%d", in.Channels, in.Rate)
	}
	if in.Channels == outChannels {
		return in, nil
	}
	frames := in.Frames()
	out := PCM{Rate: in.Rate, Channels: outChannels, Samples: make([]float32, frames*outChannels)}
	switch {
	case outChannels == 1: // average all input channels
		for f := 0; f < frames; f++ {
			var acc float32
			for c := 0; c < in.Channels; c++ {
				acc += in.Samples[f*in.Channels+c]
			}
			out.Samples[f] = acc / float32(in.Channels)
		}
	case in.Channels == 1: // replicate mono to stereo
		for f := 0; f < frames; f++ {
			v := in.Samples[f]
			out.Samples[f*2] = v
			out.Samples[f*2+1] = v
		}
	default: // wider than stereo to stereo: keep L/R, fold rest into both
		for f := 0; f < frames; f++ {
			l, r := in.Samples[f*in.Channels], in.Samples[f*in.Channels+1]
			for c := 2; c < in.Channels; c++ {
				v := in.Samples[f*in.Channels+c] / 2
				l += v
				r += v
			}
			out.Samples[f*2] = clamp(l)
			out.Samples[f*2+1] = clamp(r)
		}
	}
	return out, nil
}

func clamp(v float32) float32 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}
