package audio

import "math"

// Limiter normalizes loudness against a sliding-window RMS and clamps
// peaks to a ceiling. It records input/output peaks and applied gain so
// the pipeline can report exactly what it did to the signal.
type Limiter struct {
	window   int // frames per measurement window
	hop      int // frames per gain update
	ch       int
	target   float32 // target RMS per channel
	ceiling  float32 // absolute output peak ceiling
	energy   []float64
	head     int
	fill     int
	sum      float64
	pending  []float32 // frames buffered toward the next hop
	InPeak   float32
	OutPeak  float32
	MinGain  float32
	MaxGain  float32
	LastGain float32
}

// NewLimiter builds a limiter with a window of windowFrames (e.g. 0.1 s
// worth) updated every windowFrames/4 frames.
func NewLimiter(channels, windowFrames int, targetRMS, ceiling float32) *Limiter {
	hop := windowFrames / 4
	if hop < 1 {
		hop = 1
	}
	return &Limiter{
		window: windowFrames, hop: hop, ch: channels,
		target: targetRMS, ceiling: ceiling,
		energy:  make([]float64, windowFrames),
		MinGain: float32(math.Inf(1)), MaxGain: float32(math.Inf(-1)), LastGain: 1,
	}
}

// Process applies loudness normalization and limiting, returning a new
// slice. Input must be a whole number of frames.
func (l *Limiter) Process(in []float32) []float32 {
	out := make([]float32, 0, len(in))
	for i := 0; i < len(in); i += l.ch {
		frame := in[i : i+l.ch]
		var e float64
		for _, s := range frame {
			a := abs32(s)
			if a > l.InPeak {
				l.InPeak = a
			}
			e += float64(s) * float64(s)
		}
		// slide the window
		l.sum -= l.energy[l.head]
		l.energy[l.head] = e
		l.sum += e
		l.head = (l.head + 1) % l.window
		if l.fill < l.window {
			l.fill++
		}
		l.pending = append(l.pending, frame...)
		if len(l.pending)/l.ch >= l.hop {
			gain := l.gain()
			out = append(out, l.apply(l.pending, gain)...)
			l.pending = l.pending[:0]
		}
	}
	if len(l.pending) > 0 {
		out = append(out, l.apply(l.pending, l.gain())...)
		l.pending = l.pending[:0]
	}
	return out
}

// gain computes the hop gain from the sliding-window RMS, clamped to
// [0.25, 4] so recovery stays gradual.
func (l *Limiter) gain() float32 {
	rms := float32(math.Sqrt(l.sum / float64(l.fill*l.ch)))
	g := float32(1)
	if rms > 1e-6 {
		g = l.target / rms
	}
	g = min32(max32(g, 0.25), 4)
	l.LastGain = g
	if g < l.MinGain {
		l.MinGain = g
	}
	if g > l.MaxGain {
		l.MaxGain = g
	}
	return g
}

// apply scales a block and hard-limits to the ceiling.
func (l *Limiter) apply(block []float32, gain float32) []float32 {
	out := make([]float32, len(block))
	for i, s := range block {
		v := s * gain
		v = min32(max32(v, -l.ceiling), l.ceiling)
		if a := abs32(v); a > l.OutPeak {
			l.OutPeak = a
		}
		out[i] = v
	}
	return out
}

func abs32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

func min32(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

func max32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
