package audio

import (
	"math"
)

// Reference levels used by the kernel's loudness policy.
const (
	TargetLUFS        = -16.0
	PeakCeilingDBFS   = -1.0
	peakCeilingLinear = 0.8912509381337456 // 10^(-1/20)
	// MaxGainDB bounds runaway gain on near-silent input.
	MaxGainDB = 24.0
)

// FloatRounding documents the single rounding rule used on every float path:
// results are rounded to nearest, ties away from zero, at the point a value
// is quantised back to int16. All accumulation stays in float64.
func quantize16(v float64) int16 {
	if v >= 0 {
		v = math.Floor(v + 0.5)
	} else {
		v = math.Ceil(v - 0.5)
	}
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// LoudnessMeter accumulates mean-square energy over chunks. Interleaved
// multi-channel frames are averaged across channels per sample, matching the
// ITU-R BS.1770 channel-mean simplification used by this kernel.
type LoudnessMeter struct {
	sumSquares float64
	samples    int64
	channels   int
	gainDB     float64
	measured   bool
}

// NewLoudnessMeter builds a meter for the given channel count.
func NewLoudnessMeter(channels int) *LoudnessMeter {
	if channels < 1 {
		channels = 1
	}
	return &LoudnessMeter{channels: channels}
}

// Accumulate adds one interleaved chunk. Its length must be a whole number of
// frames; otherwise the trailing partial frame is ignored.
func (m *LoudnessMeter) Accumulate(in []int16) {
	frames := len(in) / m.channels
	for f := 0; f < frames; f++ {
		var acc float64
		for c := 0; c < m.channels; c++ {
			x := float64(in[f*m.channels+c]) / 32768.0
			acc += x * x
		}
		m.sumSquares += acc / float64(m.channels)
	}
	m.samples += int64(frames)
}

// LUFS returns the integrated loudness estimate. With no signal it returns
// -math.Inf so callers apply the bounded default gain.
func (m *LoudnessMeter) LUFS() float64 {
	if m.samples == 0 || m.sumSquares <= 0 {
		return math.Inf(-1)
	}
	ms := m.sumSquares / float64(m.samples)
	// LUFS = -0.691 + 10*log10(mean square), the BS.1770 offset.
	return -0.691 + 10*math.Log10(ms)
}

// TargetGainDB computes the gain needed to reach TargetLUFS, clamped to the
// documented maximum so silence cannot produce an unbounded boost.
func (m *LoudnessMeter) TargetGainDB() float64 {
	g := TargetLUFS - m.LUFS()
	if g > MaxGainDB {
		g = MaxGainDB
	}
	if g < -MaxGainDB {
		g = -MaxGainDB
	}
	return g
}

// ComputeGain measures the current loudness over everything accumulated and
// caches the linear gain that subsequent Apply calls use.
func (m *LoudnessMeter) ComputeGain() float64 {
	m.gainDB = m.TargetGainDB()
	m.measured = true
	return math.Pow(10, m.gainDB/20)
}

// GainDB returns the last computed gain in decibels.
func (m *LoudnessMeter) GainDB() float64 { return m.gainDB }

// Apply scales every sample by the linear gain, then enforces the -1 dBFS
// true-peak ceiling with a hard limiter (any sample beyond the ceiling is
// clamped to it). Quantisation uses quantize16's single rounding rule.
func (m *LoudnessMeter) Apply(in []int16) []int16 {
	gain := m.gainLinear()
	limit := peakCeilingLinear * 32768.0
	out := make([]int16, len(in))
	for i, v := range in {
		scaled := float64(v) * gain
		if scaled > limit {
			scaled = limit
		} else if scaled < -limit {
			scaled = -limit
		}
		out[i] = quantize16(scaled)
	}
	return out
}

func (m *LoudnessMeter) gainLinear() float64 {
	if !m.measured {
		return m.ComputeGain()
	}
	return math.Pow(10, m.gainDB/20)
}

// TruePeakDBFS returns the maximum absolute sample level in dBFS for a chunk.
func TruePeakDBFS(in []int16) float64 {
	var peak float64
	for _, v := range in {
		a := math.Abs(float64(v))
		if a > peak {
			peak = a
		}
	}
	if peak == 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(peak/32768.0)
}

// LoudnessState is the serialisable meter checkpoint.
type LoudnessState struct {
	SumSquares float64 `json:"sum_squares"`
	Samples    int64   `json:"samples"`
	Channels   int     `json:"channels"`
	GainDB     float64 `json:"gain_db"`
	Measured   bool    `json:"measured"`
}

// Snapshot captures meter state for chunked/resumable processing.
func (m *LoudnessMeter) Snapshot() LoudnessState {
	return LoudnessState{
		SumSquares: m.sumSquares, Samples: m.samples, Channels: m.channels,
		GainDB: m.gainDB, Measured: m.measured,
	}
}

// RestoreLoudness rebuilds a meter from a checkpoint.
func RestoreLoudness(s LoudnessState) *LoudnessMeter {
	return &LoudnessMeter{
		sumSquares: s.SumSquares, samples: s.Samples, channels: s.Channels,
		gainDB: s.GainDB, measured: s.Measured,
	}
}
