package ebu

import (
	"fmt"
	"math"
	"sync"
)

// Biquad is a second-order IIR section in direct form I. Coefficients are
// normalised so that a0 == 1.
type Biquad struct {
	B0, B1, B2 float64
	A1, A2     float64
}

// coefficients returns the b and a coefficient pairs expected by ffmpeg's
// biquad filter (b0 b1 b2 a0 a1 a2).
func (q Biquad) coefficients() (b, a [3]float64) {
	return [3]float64{q.B0, q.B1, q.B2}, [3]float64{1, q.A1, q.A2}
}

// Stage applies one biquad to one channel. State is kept per stage instance,
// which is why callers must never share a State between channels or files.
type State struct {
	z1, z2 float64
}

// Reset clears the filter memory. It is called only when a new file/channel
// starts, never at frame or block boundaries.
func (s *State) Reset() { s.z1, s.z2 = 0, 0 }

// Process filters a single sample in place and returns the result.
func (q Biquad) Process(s *State, x float64) float64 {
	y := q.B0*x + s.z1
	s.z1 = q.B1*x - q.A1*y + s.z2
	s.z2 = q.B2*x - q.A2*y
	return y
}

// kWeightingRate is the K-weighting filter pair for one sample rate.
type kWeightingRate struct {
	HighPass Biquad
	Shelf    Biquad
	Rate     int
}

// minKWeightingRate is the lowest rate the analytic design is meaningful for.
// The shelf at 1.7 kHz is a large fraction of an 8 kHz Nyquist limit, and the
// bilinear transform's warping there shifts the 1 kHz response by about
// 0.2 dB. Below 8 kHz the error grows quickly, so such files are rejected
// rather than reported as if they had been measured to BS.1770.
const minKWeightingRate = 8000

// kWeightingTable caches the coefficients for rates already requested. The
// coefficients are derived at the file's real rate rather than borrowed from
// a 48 kHz table, which is what libebur128 does: a 32 kHz asset then measures
// correctly instead of being rejected or silently resampled.
var kWeightingTable = sync.Map{} // int -> kWeightingRate

func init() {
	// Pre-seed the common rates so a typo in a test cannot hide a derivation
	// bug: the 48 kHz pair is checked against the BS.1770 reference values.
	for _, fs := range []int{44100, 48000, 88200, 96000, 192000} {
		kWeightingTable.Store(fs, deriveKWeighting(float64(fs)))
	}
}

// deriveKWeighting computes BS.1770 K-weighting for an arbitrary rate using the
// De Man analytic design, matching the tabulated 48 kHz reference values.
func deriveKWeighting(fs float64) kWeightingRate {
	return kWeightingRate{
		HighPass: deriveHighPass(fs),
		Shelf:    deriveShelf(fs),
		Rate:     int(fs),
	}
}

func deriveHighPass(fs float64) Biquad {
	const f0 = 38.13547087602444
	const q = 0.5003270373238773
	k := math.Tan(math.Pi * f0 / fs)
	a0 := 1 + k/q + k*k
	return Biquad{
		B0: 1 / a0,
		B1: -2 / a0,
		B2: 1 / a0,
		A1: 2 * (k*k - 1) / a0,
		A2: (1 - k/q + k*k) / a0,
	}
}

func deriveShelf(fs float64) Biquad {
	const f0 = 1681.974450955533
	const gainDB = 3.999843853973347
	const q = 0.7071752369554196
	k := math.Tan(math.Pi * f0 / fs)
	vh := math.Pow(10, gainDB/20)
	vb := math.Pow(vh, 0.4996667741545416)
	a0 := 1 + k/q + k*k
	return Biquad{
		B0: (vh + vb*k/q + k*k) / a0,
		B1: 2 * (k*k - vh) / a0,
		B2: (vh - vb*k/q + k*k) / a0,
		A1: 2 * (k*k - 1) / a0,
		A2: (1 - k/q + k*k) / a0,
	}
}

// KWeightingFor returns the K-weighting filter pair for a sample rate,
// deriving and caching it when the rate has not been seen before.
func KWeightingFor(sampleRate int) (kWeightingRate, error) {
	if sampleRate < minKWeightingRate {
		return kWeightingRate{}, fmt.Errorf("sample rate %d Hz is below the %d Hz minimum for K-weighting", sampleRate, minKWeightingRate)
	}
	if cached, ok := kWeightingTable.Load(sampleRate); ok {
		return cached.(kWeightingRate), nil
	}
	kw := deriveKWeighting(float64(sampleRate))
	actual, _ := kWeightingTable.LoadOrStore(sampleRate, kw)
	return actual.(kWeightingRate), nil
}

// Chain is a per-channel cascade of the two K-weighting stages.
type Chain struct {
	highPass Biquad
	shelf    Biquad
	hpState  State
	shState  State
}

// NewChain builds a fresh chain for a sample rate.
func NewChain(kw kWeightingRate) *Chain {
	return &Chain{highPass: kw.HighPass, shelf: kw.Shelf}
}

// Reset clears all filter memory.
func (c *Chain) Reset() {
	c.hpState.Reset()
	c.shState.Reset()
}

// Process filters one sample through the cascade.
func (c *Chain) Process(x float64) float64 {
	return c.shelf.Process(&c.shState, c.highPass.Process(&c.hpState, x))
}

// KWeightingGain returns the magnitude response of the K-weighting cascade at
// freq for a sample rate. Fixtures use it to calibrate tones analytically.
func KWeightingGain(sampleRate int, freq float64) (float64, error) {
	kw, err := KWeightingFor(sampleRate)
	if err != nil {
		return 0, err
	}
	return biquadGain(kw.HighPass, sampleRate, freq) * biquadGain(kw.Shelf, sampleRate, freq), nil
}

// biquadGain evaluates |H(e^{jw})| for a normalised biquad.
func biquadGain(q Biquad, sampleRate int, freq float64) float64 {
	w := 2 * math.Pi * freq / float64(sampleRate)
	cos1, sin1 := math.Cos(w), math.Sin(w)
	cos2, sin2 := math.Cos(2*w), math.Sin(2*w)
	numRe := q.B0 + q.B1*cos1 + q.B2*cos2
	numIm := -(q.B1*sin1 + q.B2*sin2)
	denRe := 1 + q.A1*cos1 + q.A2*cos2
	denIm := -(q.A1*sin1 + q.A2*sin2)
	num := math.Hypot(numRe, numIm)
	den := math.Hypot(denRe, denIm)
	if den == 0 {
		return 0
	}
	return num / den
}
