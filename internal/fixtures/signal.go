package fixtures

import (
	"encoding/binary"
	"math"
	"math/rand"
)

// Signal describes the PCM tone content of a fixture.
type Signal struct {
	// Freq is the tone frequency in Hz; 0 means silence.
	Freq float64
	// Amplitude is the linear peak amplitude in [0,1].
	Amplitude float64
	// Noise is the linear amplitude of additive white noise.
	Noise float64
	// PhaseRadians offsets the tone phase.
	PhaseRadians float64
	// ClippingAfterGain, when > 0, clips samples at that gain factor to mimic
	// a hot master that must be tamed by the loudness stage.
	ClippingAfterGain float64
}

// PCM writes interleaved s16le samples with the channel layout's weights.
// Each channel gets the tone with a fixed phase offset so the layout merge can
// be checked for cross-channel cancellation.
func (s Signal) render(frames, channels, sampleRate int, layout string, rng *rand.Rand) []byte {
	out := make([]byte, frames*channels*2)
	weights := channelWeights(layout)
	for f := 0; f < frames; f++ {
		tSec := float64(f) / float64(sampleRate)
		var base float64
		if s.Freq > 0 {
			base = s.Amplitude * math.Sin(2*math.Pi*s.Freq*tSec+s.PhaseRadians)
		}
		for c := 0; c < channels; c++ {
			v := base
			if s.Freq > 0 && c < len(weights) {
				// small per-channel phase shift keeps channels correlated but
				// distinct; no channel is left dead.
				v = weights[c] * s.Amplitude * math.Sin(2*math.Pi*s.Freq*tSec+s.PhaseRadians+0.05*float64(c))
			}
			if s.Noise > 0 {
				v += s.Noise * (rng.Float64()*2 - 1)
			}
			if s.ClippingAfterGain > 1 {
				v *= s.ClippingAfterGain
			}
			if v > 1 {
				v = 1
			} else if v < -1 {
				v = -1
			}
			iv := int16(math.Round(v * 32767))
			binary.LittleEndian.PutUint16(out[(f*channels+c)*2:], uint16(iv))
		}
	}
	return out
}

func channelWeights(layout string) []float64 {
	switch layout {
	case "mono":
		return []float64{1}
	case "5.1":
		return []float64{1, 0.9, 0.9, 0.7, 0.7, 0.4}
	default: // stereo and anything else
		return []float64{1, 0.92}
	}
}

// silenceBytes returns a silent interleaved PCM buffer.
func silenceBytes(frames, channels int) []byte {
	return make([]byte, frames*channels*2)
}
