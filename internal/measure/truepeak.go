package measure

import "math"

// True-peak oversampling parameters: 4x windowed-sinc interpolation
// with 16 taps of context on each side.
const (
	tpOversample = 4
	tpHalf       = 16
)

// TruePeakMeter estimates inter-sample true peak per channel via 4x
// oversampling. State is O(channels); block boundaries are handled by
// carrying unprocessed context across AddBlock calls.
type TruePeakMeter struct {
	coeffs  [tpOversample - 1][]float64 // phases 1/4, 2/4, 3/4
	buf     [][]float64                 // per channel: left context + pending
	pos     []int                       // next center-sample index in buf
	unproc  []int                       // real samples not yet centered
	max     []float64
	frames  int64
	flushed bool
}

// NewTruePeakMeter builds a meter for the given channel count.
func NewTruePeakMeter(channels int) *TruePeakMeter {
	m := &TruePeakMeter{
		buf:    make([][]float64, channels),
		pos:    make([]int, channels),
		unproc: make([]int, channels),
		max:    make([]float64, channels),
	}
	for p := 1; p < tpOversample; p++ {
		m.coeffs[p-1] = sincCoeffs(float64(p) / tpOversample)
	}
	for c := range m.buf {
		m.buf[c] = make([]float64, tpHalf-1) // zero left context
		m.pos[c] = tpHalf - 1
	}
	return m
}

// sincCoeffs returns 2*tpHalf windowed-sinc taps for fractional delay
// phase (0 < phase < 1), normalized to unit DC gain.
func sincCoeffs(phase float64) []float64 {
	n := 2 * tpHalf
	c := make([]float64, n)
	var sum float64
	for j := 0; j < n; j++ {
		d := phase + float64(tpHalf-1) - float64(j)
		var s float64
		if d != 0 {
			s = math.Sin(math.Pi*d) / (math.Pi * d)
		} else {
			s = 1
		}
		// Blackman window over |d| <= tpHalf.
		w := 0.42 + 0.5*math.Cos(math.Pi*d/tpHalf) + 0.08*math.Cos(2*math.Pi*d/tpHalf)
		c[j] = s * w
		sum += c[j]
	}
	for j := range c {
		c[j] /= sum
	}
	return c
}

// AddBlock feeds one deinterleaved block (samples[channel][frame]).
func (m *TruePeakMeter) AddBlock(samples [][]float64) {
	if len(samples) == 0 {
		return
	}
	for c := range samples {
		m.buf[c] = append(m.buf[c], samples[c]...)
		m.unproc[c] += len(samples[c])
		m.drain(c, false)
	}
	m.frames += int64(len(samples[0]))
}

// Flush resolves the trailing samples with zero right-padding.
func (m *TruePeakMeter) Flush() {
	if m.flushed {
		return
	}
	m.flushed = true
	for c := range m.buf {
		m.buf[c] = append(m.buf[c], make([]float64, tpHalf)...)
		m.drain(c, true)
	}
}

func (m *TruePeakMeter) drain(c int, final bool) {
	buf := m.buf[c]
	for m.unproc[c] > 0 {
		p := m.pos[c]
		if p+tpHalf >= len(buf) && !final {
			break // wait for more right context
		}
		if v := math.Abs(buf[p]); v > m.max[c] {
			m.max[c] = v
		}
		for ph := 0; ph < tpOversample-1; ph++ {
			co := m.coeffs[ph]
			var acc float64
			for j := 0; j < 2*tpHalf; j++ {
				k := p - (tpHalf - 1) + j
				var x float64
				if k >= 0 && k < len(buf) {
					x = buf[k]
				}
				acc += co[j] * x
			}
			if v := math.Abs(acc); v > m.max[c] {
				m.max[c] = v
			}
		}
		m.pos[c]++
		m.unproc[c]--
	}
	// Compact consumed prefix, keeping tpHalf-1 samples of left context.
	if m.pos[c] >= 4096 {
		start := m.pos[c] - (tpHalf - 1)
		copy(buf, buf[start:m.pos[c]+tpHalf])
		m.buf[c] = buf[:len(buf)-start]
		m.pos[c] = tpHalf - 1
	}
}

// MaxLinear returns the per-channel true peak as a linear magnitude.
func (m *TruePeakMeter) MaxLinear() []float64 {
	m.Flush()
	out := make([]float64, len(m.max))
	copy(out, m.max)
	return out
}

// DBTP returns the overall true peak in dBTP across all channels, or
// nil when there is no signal (empty input or digital silence).
func (m *TruePeakMeter) DBTP() *float64 {
	m.Flush()
	if m.frames == 0 {
		return nil
	}
	var peak float64
	for _, v := range m.max {
		if v > peak {
			peak = v
		}
	}
	if peak == 0 {
		return nil
	}
	v := 20 * math.Log10(peak)
	return &v
}
