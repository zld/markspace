package peak

import (
	"errors"
	"fmt"
	"math"
)

// maxChannels bounds the channel count the DSP path accepts, matching the
// loudness module's ceiling.
const maxChannels = 8

// Oversampling configuration. Four phases are the BS.1770-4 minimum. The
// kernel is a Blackman-Harris windowed sinc with 12 taps per phase, which is
// flat to well within 0.1 dB across the pass band and deep in the stop band,
// so inter-sample peaks are neither missed nor inflated by aliasing images.
const (
	oversampleFactor = 4
	tapsPerPhase     = 12
	// centre is the window index of the sample the phases start from.
	centre = tapsPerPhase/2 - 1
	// span is the number of frames one interpolation window consumes.
	span = tapsPerPhase
)

// kernel[phase][k] is the FIR coefficient applied to window frame k when
// interpolating at fraction phase/oversampleFactor after the centre frame.
// Phase 0 is the centre sample itself and is measured directly from the PCM.
var kernel [oversampleFactor][tapsPerPhase]float64

func init() {
	for phase := 1; phase < oversampleFactor; phase++ {
		frac := float64(phase) / float64(oversampleFactor)
		var sum float64
		for k := 0; k < tapsPerPhase; k++ {
			d := frac - float64(k-centre)
			kernel[phase][k] = sinc(d) * blackmanHarris(k, tapsPerPhase)
			sum += kernel[phase][k]
		}
		// Unity DC gain so flat passages keep their level.
		for k := range kernel[phase] {
			kernel[phase][k] /= sum
		}
	}
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	px := math.Pi * x
	return math.Sin(px) / px
}

func blackmanHarris(i, n int) float64 {
	const a0, a1, a2, a3 = 0.35875, 0.48829, 0.14128, 0.01168
	x := 2 * math.Pi * float64(i) / float64(n-1)
	return a0 - a1*math.Cos(x) + a2*math.Cos(2*x) - a3*math.Cos(3*x)
}

// Meter tracks the true peak of interleaved PCM across arbitrary chunk
// boundaries. It retains the first and last span frames plus the rolling
// window, so memory is bounded no matter how long the file is.
type Meter struct {
	channels int
	frames   int64
	// carry holds frames base .. frames-1 interleaved.
	carry []float64
	// base is the global index of carry[0]. Windows are evaluated for every
	// centre frame from centre through base+centre-1.
	base int64
	// head and tail retain the file edges for the boundary windows.
	head []float64
	tail []float64

	peak       float64
	peakCh     int
	samplePeak float64
}

// NewMeter creates a true-peak meter for a channel count.
func NewMeter(channels int) (*Meter, error) {
	if channels < 1 || channels > maxChannels {
		return nil, fmt.Errorf("unsupported channel count %d for true peak (max %d)", channels, maxChannels)
	}
	return &Meter{
		channels: channels,
		carry:    make([]float64, 0, span*channels),
		peakCh:   -1,
	}, nil
}

// Process consumes one interleaved chunk. Any chunk size that is a whole
// number of frames is valid; the window is carried across calls so chunk
// edges never create or hide a peak.
func (m *Meter) Process(frames []float64) error {
	ch := m.channels
	if len(frames)%ch != 0 {
		return errors.New("pcm buffer is not a whole number of frames")
	}
	for _, x := range frames {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return errors.New("non-finite sample in decoded pcm")
		}
		if a := math.Abs(x); a > m.samplePeak {
			m.samplePeak = a
		}
	}
	if room := span*ch - len(m.head); room > 0 {
		take := room
		if take > len(frames) {
			take = len(frames)
		}
		m.head = append(m.head, frames[:take]...)
	}
	m.tail = append(m.tail, frames...)
	if keep := span * ch; len(m.tail) > keep {
		trimmed := make([]float64, keep, keep)
		copy(trimmed, m.tail[len(m.tail)-keep:])
		m.tail = trimmed
	}

	m.carry = append(m.carry, frames...)
	m.frames += int64(len(frames) / ch)

	need := span * ch
	for len(m.carry) >= need {
		m.evaluate(m.carry[:need])
		m.carry = m.carry[ch:]
		m.base++
	}
	// Bound carry capacity no matter how large the caller's chunks are.
	if cap(m.carry) > 4*need {
		carry := make([]float64, len(m.carry), need)
		copy(carry, m.carry)
		m.carry = carry
	}
	return nil
}

// evaluate measures the discrete centre sample plus the interpolated phases
// between the centre frame and the next one.
func (m *Meter) evaluate(win []float64) {
	ch := m.channels
	for c := 0; c < ch; c++ {
		m.consider(win[centre*ch+c], c)
		for phase := 1; phase < oversampleFactor; phase++ {
			var v float64
			for k := 0; k < tapsPerPhase; k++ {
				v += kernel[phase][k] * win[k*ch+c]
			}
			m.consider(v, c)
		}
	}
}

func (m *Meter) consider(v float64, c int) {
	if a := math.Abs(v); a > m.peak {
		m.peak = a
		m.peakCh = c
	}
}

// Result holds the true-peak measurement.
type Result struct {
	// Linear is the peak magnitude; 1.0 is 0 dBFS.
	Linear float64
	// DBTP is 20*log10(Linear). Positive values mean the reconstructed
	// waveform overshoots full scale and are reported, never clamped.
	DBTP float64
	// Channel is the zero-based channel holding the peak, or -1 for silence.
	Channel int
	Frames  int64
	// SamplePeak is the largest discrete PCM sample magnitude, which tells
	// inter-sample overshoot apart from plain sample clipping.
	SamplePeak float64
}

// Finish measures the boundary windows Process could not complete: the first
// `centre` centres and the last ones whose window reached past end-of-file.
// Frames outside the file are mirrored (x[-k] = x[k], x[n-1+j] = x[n-1-j]);
// mirroring keeps a non-zero ending continuous and cannot manufacture a peak
// from the artificial step that zero padding would create. Every real sample
// is also counted exactly as a discrete peak.
func (m *Meter) Finish() Result {
	n := m.frames
	if n > 0 {
		for c := int64(0); c < centre && c < n; c++ {
			m.evaluateAt(c)
		}
		for c := m.base + centre; c < n; c++ {
			m.evaluateAt(c)
		}
	}
	res := Result{
		Linear:     m.peak,
		Channel:    m.peakCh,
		Frames:     m.frames,
		SamplePeak: m.samplePeak,
	}
	if m.peak > 0 {
		res.DBTP = 20 * math.Log10(m.peak)
	} else {
		res.DBTP = math.Inf(-1)
	}
	return res
}

// evaluateAt measures the window whose centre frame is c.
func (m *Meter) evaluateAt(c int64) {
	ch := m.channels
	win := make([]float64, span*ch)
	for k := 0; k < span; k++ {
		g := c - centre + int64(k)
		for ich := 0; ich < ch; ich++ {
			win[k*ch+ich] = m.frameAt(g, ich)
		}
	}
	m.evaluate(win)
}

// frameAt returns frame g of channel c, reflecting indices outside the file.
// Boundary windows only ever touch the retained head, the tail or the carry.
func (m *Meter) frameAt(g int64, c int) float64 {
	ch := int64(m.channels)
	n := m.frames
	if g < 0 {
		g = -g
	}
	if g >= n {
		g = 2*n - 2 - g
	}
	if g < 0 {
		g = 0
	}
	if g >= n {
		g = n - 1
	}
	if headFrames := int64(len(m.head)) / ch; g < headFrames {
		return m.head[g*ch+int64(c)]
	}
	tailFrames := int64(len(m.tail)) / ch
	if tailStart := n - tailFrames; g >= tailStart {
		return m.tail[(g-tailStart)*ch+int64(c)]
	}
	pos := g - m.base
	if pos < 0 {
		pos = 0
	}
	if pos >= int64(len(m.carry))/ch {
		pos = int64(len(m.carry))/ch - 1
	}
	return m.carry[pos*ch+int64(c)]
}
