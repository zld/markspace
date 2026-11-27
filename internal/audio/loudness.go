package audio

import (
	"encoding/binary"
	"math"
)

func bytesToInt16(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out
}

func int16ToBytes(s []int16) []byte {
	out := make([]byte, 2*len(s))
	for i, v := range s {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out
}

// measureLUFS computes a deterministic integrated-loudness approximation:
// the RMS of all channels expressed in dBFS. It is not a full ITU-R BS.1770
// K-weighted meter, but it is reproducible and adequate for relative
// loudness normalization of archive material.
func measureLUFS(samples []int16, channels int) float64 {
	if len(samples) == 0 || channels == 0 {
		return 0
	}
	var sumSq float64
	for _, s := range samples {
		v := float64(s) / 32768.0
		sumSq += v * v
	}
	rms := math.Sqrt(sumSq / float64(len(samples)))
	if rms <= 1e-9 {
		return -math.Inf(1)
	}
	return 20 * math.Log10(rms)
}

// loudnessGain returns the linear gain reaching the configured target LUFS.
// A zero target disables normalization. Gain is bounded to avoid runaway on
// near-silent input.
func (c *Converter) loudnessGain(samples []int16) float64 {
	if c.cfg.TargetLUFS == 0 || len(samples) == 0 {
		return 1
	}
	lufs := measureLUFS(samples, c.dstCh)
	if math.IsInf(lufs, -1) {
		return 1
	}
	db := c.cfg.TargetLUFS - lufs
	gain := math.Pow(10, db/20)
	const maxGain = 20 // +26 dB safety ceiling
	if gain > maxGain {
		gain = maxGain
	}
	if gain < 0 {
		gain = 0
	}
	return gain
}

// applyGain scales every sample, tracks the peak and counts any value that
// hits the 16-bit rails as clipping.
func (c *Converter) applyGain(samples []int16, gain float64) []int16 {
	out := make([]int16, len(samples))
	for i, s := range samples {
		if peak := abs32(int32(s)); peak > c.stats.Peak {
			c.stats.Peak = peak
		}
		v := math.Round(float64(s) * gain)
		switch {
		case v > math.MaxInt16:
			out[i] = math.MaxInt16
			c.stats.Clips++
		case v < math.MinInt16:
			out[i] = math.MinInt16
			c.stats.Clips++
		default:
			out[i] = int16(v)
		}
	}
	return out
}

func abs32(v int32) int32 {
	if v < 0 {
		// MinInt16 can't appear from PCM16 input, so this stays safe.
		return -v
	}
	return v
}

// MarkUndecodable lets an outer decoder report a payload it could not decode,
// keeping the failure visible in the shared stats.
func (c *Converter) MarkUndecodable(n int) {
	c.stats.Undecodable += uint64(n)
}

// Reset clears per-run counters (carry is retained for timeline continuity).
func (c *Converter) Reset() {
	c.stats = Stats{}
}
