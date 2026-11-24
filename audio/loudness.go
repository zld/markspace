package audio

import "math"

// SilenceFloorLUFS is the reported integrated loudness for digital silence.
const SilenceFloorLUFS = -70.0

// LoudnessStats is the loudness report for a processed audio stream.
type LoudnessStats struct {
	IntegratedLUFS float64
	LoudnessDelta  float64 // measured minus target, after normalization
	TruePeak       float32 // maximum absolute sample, after normalization
	Clips          int64   // samples hard-clipped at the peak limit
	Samples        int64
}

// Meter accumulates squared energy and peak values across frames. The LUFS
// figure follows the BS.1770 energy formula without the K-weighting stage,
// which is a documented approximation acceptable for self-generated signals.
type Meter struct {
	sumSq float64
	peak  float32
	n     int64
}

// Add folds one frame into the running statistics.
func (m *Meter) Add(f Frame) {
	for _, s := range f.Samples {
		a := s
		if a < 0 {
			a = -a
		}
		m.sumSq += float64(s) * float64(s)
		if a > m.peak {
			m.peak = a
		}
		m.n++
	}
}

// Reset clears all accumulated measurements.
func (m *Meter) Reset() { m.sumSq, m.peak, m.n = 0, 0, 0 }

// Stats computes the report. An empty or silent stream reports the floor.
func (m *Meter) Stats() LoudnessStats {
	if m.n == 0 || m.sumSq == 0 {
		return LoudnessStats{IntegratedLUFS: SilenceFloorLUFS, Samples: m.n}
	}
	meanSq := m.sumSq / float64(m.n)
	return LoudnessStats{
		IntegratedLUFS: -0.691 + 10*math.Log10(meanSq),
		TruePeak:       m.peak,
		Samples:        m.n,
	}
}

// Normalizer applies a static gain to reach a target integrated loudness and
// hard-clips samples that exceed the configured true-peak limit, recording
// every clipped sample instead of letting it overflow silently.
type Normalizer struct {
	TargetLUFS    float64
	TruePeakLimit float32
	pre           Meter
	post          Meter
	gainApplied   bool
	gain          float32
	clips         int64
}

// NewNormalizer builds a normalizer. peakLimit <= 0 selects the full scale.
func NewNormalizer(targetLUFS float64, peakLimit float32) *Normalizer {
	if peakLimit <= 0 {
		peakLimit = 1.0
	}
	return &Normalizer{TargetLUFS: targetLUFS, TruePeakLimit: peakLimit}
}

// Measure first: feed every input frame before calling Normalize so the gain
// is computed from the full-stream integrated loudness.
func (n *Normalizer) Measure(f Frame) { n.pre.Add(f) }

// Normalize applies the target gain and peak limiter to one frame.
func (n *Normalizer) Normalize(in Frame) Frame {
	if !n.gainApplied {
		pre := n.pre.Stats()
		if pre.IntegratedLUFS > SilenceFloorLUFS {
			n.gain = float32(math.Pow(10, (n.TargetLUFS-pre.IntegratedLUFS)/20))
		} else {
			n.gain = 1
		}
		n.gainApplied = true
	}
	out := make([]float32, len(in.Samples))
	lim := n.TruePeakLimit
	for i, s := range in.Samples {
		v := s * n.gain
		if v > lim {
			v = lim
			n.clips++
		} else if v < -lim {
			v = -lim
			n.clips++
		}
		out[i] = v
	}
	n.post.Add(Frame{Samples: out, Channels: in.Channels, Rate: in.Rate})
	return Frame{Samples: out, Channels: in.Channels, Rate: in.Rate, PTS90: in.PTS90}
}

// Stats returns the post-normalization loudness report including the
// deviation from the target and the total clipped-sample count.
func (n *Normalizer) Stats() LoudnessStats {
	st := n.post.Stats()
	st.LoudnessDelta = st.IntegratedLUFS - n.TargetLUFS
	st.Clips = n.clips
	return st
}
