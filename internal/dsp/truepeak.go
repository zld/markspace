package dsp

import "math"

// True-peak oversampling parameters. Four phases are evaluated per input
// sample: the input sample itself plus three interpolated points between it
// and the next sample.
const (
	// Oversample is the true-peak oversampling factor.
	Oversample = 4
	interpTaps = 8 // half width of the windowed-sinc kernel
	interpBeta = 12.0
)

// truePeakPhases[p-1][k] interpolates the signal at a fraction p/4 of a
// sample after position n, using samples n+k for k in [-interpTaps, interpTaps].
var truePeakPhases = buildPhases()

func buildPhases() [Oversample - 1][]float64 {
	var phases [Oversample - 1][]float64
	norm := besselI0(interpBeta)
	for p := 1; p < Oversample; p++ {
		row := make([]float64, 2*interpTaps+1)
		d := float64(p) / Oversample
		var sum float64
		for i := 0; i <= 2*interpTaps; i++ {
			k := i - interpTaps
			x := d - float64(k)
			s := 1.0
			if x != 0 {
				s = math.Sin(math.Pi*x) / (math.Pi * x)
			}
			r := x / interpTaps
			w := 0.0
			if r > -1 && r < 1 {
				w = besselI0(interpBeta*math.Sqrt(1-r*r)) / norm
			}
			row[i] = s * w
			sum += row[i]
		}
		// Force unity gain at DC; the truncated sinc is slightly off.
		for i := range row {
			row[i] /= sum
		}
		phases[p-1] = row
	}
	return phases
}

func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	y := x * x / 4
	for k := 1; k < 64; k++ {
		term *= y / float64(k*k)
		sum += term
		if term < sum*1e-17 {
			break
		}
	}
	return sum
}

// TruePeak detects inter-sample peaks of one channel by 4x band-limited
// interpolation. Samples are fed in order and the detector keeps only a small
// history, so memory use is independent of file length.
//
// Boundary handling is explicit: interpolation runs only at positions whose
// kernel fits entirely inside the stream. Padding the kernel with zeros would
// ring against the cut, and mirroring the stream would repeat the edge sample
// and duplicate a transient; either can report a peak the audio does not
// contain, which is the one error a delivery gate must not make. Both edges
// are still covered, because every raw sample is itself a true-peak
// candidate, so a one-sample transient at the first or last position is
// reported. At 48 kHz the interpolated rim that is skipped is 0.17 ms at each
// end, and only 8 samples of lookahead plus history are retained, so the
// result does not depend on how the stream was split into Write calls.
type TruePeak struct {
	buf   []float64
	base  int64 // absolute index of buf[0]
	next  int64 // next absolute sample position to evaluate
	total int64 // samples written so far

	samplePeak float64
	peak       float64

	done bool
}

// NewTruePeak returns a detector with peak and history reset.
func NewTruePeak() *TruePeak {
	return &TruePeak{buf: make([]float64, 0, 2*interpTaps+4096), next: -int64(interpTaps)}
}

// Write feeds the next block of samples for a single channel.
func (t *TruePeak) Write(x []float64) {
	if t.done || len(x) == 0 {
		return
	}
	for _, v := range x {
		if a := math.Abs(v); a > t.samplePeak {
			t.samplePeak = a
		}
	}
	t.buf = append(t.buf, x...)
	t.total += int64(len(x))

	for t.next <= t.total-1-interpTaps {
		if t.next >= 0 {
			t.eval(t.next)
		}
		t.next++
	}
	t.prune()
}

func (t *TruePeak) prune() {
	drop := int(t.next - int64(interpTaps) - t.base)
	if drop <= 0 {
		return
	}
	if drop > len(t.buf) {
		drop = len(t.buf)
	}
	n := copy(t.buf, t.buf[drop:])
	t.buf = t.buf[:n]
	t.base += int64(drop)
}

// eval computes the peak at position p and at the three interpolated points
// that follow it. Positions without a full kernel on both sides are skipped.
func (t *TruePeak) eval(p int64) {
	center := int(p - t.base)
	if center < interpTaps || center+interpTaps >= len(t.buf) {
		return
	}
	if a := math.Abs(t.buf[center]); a > t.peak {
		t.peak = a
	}
	for _, row := range truePeakPhases {
		var acc float64
		for k := 0; k <= 2*interpTaps; k++ {
			acc += row[k] * t.buf[center-interpTaps+k]
		}
		if a := math.Abs(acc); a > t.peak {
			t.peak = a
		}
	}
}

// Finish seals the detector. Samples inside the final kernel margin are
// already included in the sample peak, so nothing is extrapolated.
func (t *TruePeak) Finish() { t.done = true }

// SamplePeak returns the largest absolute input sample seen.
func (t *TruePeak) SamplePeak() float64 { return t.samplePeak }

// Peak returns the largest absolute value among the input samples and the
// interpolated points. It is never smaller than the sample peak.
func (t *TruePeak) Peak() float64 {
	if t.samplePeak > t.peak {
		return t.samplePeak
	}
	return t.peak
}
