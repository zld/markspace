// Package quality computes audio quality metrics over retained PCM
// blocks. Corrupt or isolated ranges never contribute samples.
package quality

import (
	"math"

	"rtx/internal/wav"
)

// Metrics summarizes the valid portion of a parsed WAV stream.
type Metrics struct {
	Peak        float64 `json:"peak"`         // max absolute amplitude, [0,1]
	RMS         float64 `json:"rms"`          // root mean square amplitude, [0,1]
	Clipped     float64 `json:"clipped"`      // share of samples at full scale
	Coverage    float64 `json:"coverage"`     // valid samples / expected samples
	Silence     float64 `json:"silence"`      // share of near-zero samples
	Score       float64 `json:"score"`        // composite quality score, [0,1]
	SampleCount int64   `json:"sample_count"` // valid per-channel samples
}

// fullScale maps bit width to its positive full-scale value.
func fullScale(bits uint16) float64 {
	switch bits {
	case 16:
		return 32768
	case 24:
		return 1 << 23
	case 32:
		return math.MaxInt32 + 1
	default:
		return 1
	}
}

// Measure computes metrics over blocks. expectedSamples is the number of
// per-channel samples the stream should have contained; corrupt ranges
// reduce Coverage but never contribute fabricated samples.
func Measure(blocks []wav.Block, bits uint16, expectedSamples int64) *Metrics {
	m := &Metrics{}
	fs := fullScale(bits)
	var sumSq float64
	var count, clipped, silence int64
	clipAt := 0.999 * fs
	silenceAt := 0.001 * fs
	for _, b := range blocks {
		for _, s := range b.Samples {
			v := float64(s)
			if v < 0 {
				v = -v
			}
			if v/fs > m.Peak {
				m.Peak = v / fs
			}
			sumSq += v * v
			count++
			if v >= clipAt {
				clipped++
			}
			if v <= silenceAt {
				silence++
			}
		}
	}
	if count == 0 {
		return m
	}
	m.SampleCount = count
	m.RMS = math.Sqrt(sumSq/float64(count)) / fs
	m.Clipped = float64(clipped) / float64(count)
	m.Silence = float64(silence) / float64(count)
	if expectedSamples > 0 {
		m.Coverage = float64(count) / float64(expectedSamples)
		if m.Coverage > 1 {
			m.Coverage = 1
		}
	}
	// Composite score: coverage dominates; clipping and a dead-silent
	// or overdriven signal reduce it.
	m.Score = m.Coverage * (1 - m.Clipped)
	if m.Peak > 0.999 {
		m.Score *= 0.9
	}
	if m.Silence > 0.95 {
		m.Score *= 0.5
	}
	return m
}
