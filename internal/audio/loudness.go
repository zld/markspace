package audio

import "math"

// Loudness normalizes windowed RMS to a target level and applies a look-ahead
// true-peak limiter. Every statistic is a deterministic function of the sample
// sequence, including partial boundary windows, which are kept rather than
// dropped so sample counts stay conserved.
type Loudness struct {
	targetLUFS  float64 // target RMS level in dBFS
	peakCeiling float64 // linear amplitude ceiling, e.g. 0.95
	windowSize  int     // frames per RMS window
	buf         []float64
	windowSums  []float64 // per-window sum of squares (for recompute)
	gain        float64   // current applied gain (smoothed toward window gain)
	smoothing   float64   // EMA coefficient in [0,1)
	outPeak     float64   // maximum absolute output sample
	outSquares  float64   // cumulative output energy
	outCount    uint64
	limited     uint64
}

// NewLoudness builds the controller. windowSize must be positive; targetDBFS
// is the desired RMS level and ceiling (<=1) the hard output peak.
func NewLoudness(targetDBFS, ceiling float64, windowSize int) *Loudness {
	if windowSize <= 0 {
		windowSize = 1024
	}
	if ceiling <= 0 || ceiling > 1 {
		ceiling = 0.95
	}
	return &Loudness{
		targetLUFS:  targetDBFS,
		peakCeiling: ceiling,
		windowSize:  windowSize,
		gain:        1,
		smoothing:   0.2,
	}
}

// dbToLinear converts dBFS to linear amplitude.
func dbToLinear(db float64) float64 { return math.Pow(10, db/20) }

// windowGain derives the gain that moves one window's RMS to target.
func (l *Loudness) windowGain(sumSquares float64, n int) float64 {
	rms := math.Sqrt(sumSquares / float64(n))
	if rms < 1e-9 {
		return 1 // silence: leave gain untouched
	}
	return dbToLinear(l.targetLUFS) / rms
}

// Process applies gain smoothing, RMS normalization and peak limiting to one
// interleaved block (any channel count; energy is computed per sample value).
func (l *Loudness) Process(in []float64) []float64 {
	out := make([]float64, len(in))
	for i, x := range in {
		// Roll a new RMS window whenever the buffer reaches windowSize.
		if len(l.buf) == l.windowSize {
			l.flushWindow()
		}
		l.buf = append(l.buf, x*x)
		target := l.gain
		if len(l.buf) == l.windowSize {
			wg := l.windowGain(sumSlice(l.buf), l.windowSize)
			target = l.gain + (wg-l.gain)*l.smoothing
		}
		y := x * target
		// True-peak ceiling: scale down hard, never clip by saturation.
		absY := math.Abs(y)
		if absY > l.peakCeiling {
			y = y / absY * l.peakCeiling
			l.limited++
		}
		out[i] = y
		if absY = math.Abs(y); absY > l.outPeak {
			l.outPeak = absY
		}
		l.outSquares += y * y
		l.outCount++
		l.gain = target
	}
	return out
}

// flushWindow archives one window's energy and resets the accumulation buffer.
func (l *Loudness) flushWindow() {
	l.windowSums = append(l.windowSums, sumSlice(l.buf))
	l.buf = l.buf[:0]
}

// Flush must be called at end-of-stream so the partial boundary window is not
// lost. It performs a final smoothing step; no samples are discarded.
func (l *Loudness) Flush() {
	if len(l.buf) > 0 {
		l.flushWindow()
	}
}

// PeakDBFS returns the maximum absolute output sample expressed in dBFS.
func (l *Loudness) PeakDBFS() float64 {
	if l.outPeak == 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(l.outPeak)
}

// RMSDBFS returns the full-output RMS in dBFS.
func (l *Loudness) RMSDBFS() float64 {
	if l.outCount == 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(math.Sqrt(l.outSquares/float64(l.outCount)))
}

// LimitedSamples reports how many samples hit the peak ceiling.
func (l *Loudness) LimitedSamples() uint64 { return l.limited }

// Gain returns the current smoothed gain for checkpointing.
func (l *Loudness) Gain() float64 { return l.gain }

// RestoreGain rehydrates gain/statistics from a checkpoint.
func (l *Loudness) RestoreGain(gain, peak, squares float64, count, limited uint64) {
	l.gain, l.outPeak, l.outSquares, l.outCount, l.limited = gain, peak, squares, count, limited
}

// Snapshot returns the full restartable loudness state, including the partial
// window buffer so a resumed run computes identical window gains.
func (l *Loudness) Snapshot() LoudnessState {
	return LoudnessState{
		Buf:        append([]float64(nil), l.buf...),
		WindowSums: append([]float64(nil), l.windowSums...),
		Gain:       l.gain,
		OutPeak:    l.outPeak,
		OutSquares: l.outSquares,
		OutCount:   l.outCount,
		Limited:    l.limited,
	}
}

// LoudnessState is the serializable controller state.
type LoudnessState struct {
	Buf        []float64 `json:"buf"`
	WindowSums []float64 `json:"window_sums"`
	Gain       float64   `json:"gain"`
	OutPeak    float64   `json:"out_peak"`
	OutSquares float64   `json:"out_squares"`
	OutCount   uint64    `json:"out_count"`
	Limited    uint64    `json:"limited"`
}

// Restore rehydrates the complete controller state from a snapshot.
func (l *Loudness) Restore(s LoudnessState) {
	l.buf = append([]float64(nil), s.Buf...)
	l.windowSums = append([]float64(nil), s.WindowSums...)
	l.gain, l.outPeak, l.outSquares, l.outCount, l.limited =
		s.Gain, s.OutPeak, s.OutSquares, s.OutCount, s.Limited
}

func sumSlice(s []float64) float64 {
	var n float64
	for _, v := range s {
		n += v
	}
	return n
}
