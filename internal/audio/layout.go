package audio

import (
	"errors"
	"math"
)

// ToStereo normalizes channel layout onto the mandatory stereo output.
// Mono is duplicated to both channels preserving identical L/R content;
// stereo keeps the original left/right order; any other layout is
// explicitly rejected rather than down-mixed by guesswork.
func ToStereo(samples []float32, channels uint16) ([]float32, error) {
	switch channels {
	case 1:
		out := make([]float32, 0, len(samples)*2)
		for _, v := range samples {
			out = append(out, v, v)
		}
		return out, nil
	case 2:
		out := make([]float32, len(samples))
		copy(out, samples)
		return out, nil
	default:
		return nil, errors.New("only mono (1) or stereo (2) AAC-LC/Opus layouts are accepted")
	}
}

// Peak returns the maximum absolute sample amplitude in linear scale.
func Peak(samples []float32) float32 {
	var peak float32
	for _, v := range samples {
		if a := float32(math.Abs(float64(v))); a > peak {
			peak = a
		}
	}
	return peak
}

// RMS returns the root-mean-square amplitude over the valid (non-silent
// window is the caller's responsibility) samples in linear scale.
func RMS(samples []float32) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, v := range samples {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(samples)))
}

// DBFS converts a linear amplitude to decibels relative to full scale.
func DBFS(linear float64) float64 {
	if linear <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(linear)
}
