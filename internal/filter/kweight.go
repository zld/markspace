// Package filter implements the BS.1770 K-weighting filter chain as
// two biquad sections with per-channel state carried across blocks.
package filter

import (
	"fmt"
	"math"
)

// Supported sample-rate range in Hz.
const (
	MinSampleRate = 8000
	MaxSampleRate = 384000
)

// Biquad is a second-order IIR section (Direct Form I) with state.
type Biquad struct {
	B0, B1, B2, A1, A2 float64
	x1, x2, y1, y2     float64
}

// Process advances the filter by one sample.
func (b *Biquad) Process(x float64) float64 {
	y := b.B0*x + b.B1*b.x1 + b.B2*b.x2 - b.A1*b.y1 - b.A2*b.y2
	b.x2, b.x1 = b.x1, x
	b.y2, b.y1 = b.y1, y
	return y
}

// Stable reports whether the section's poles lie inside the unit circle
// (Jury criterion for a second-order denominator 1 + A1 z^-1 + A2 z^-2).
func (b *Biquad) Stable() bool {
	return math.Abs(b.A2) < 1 && math.Abs(b.A1) < 1+b.A2
}

// KWeight chains the BS.1770 pre-filter (high shelf) and RLB high-pass.
type KWeight struct {
	shelf Biquad
	hp    Biquad
}

// NewKWeight derives K-weighting coefficients for the given sample rate.
// It fails for unsupported rates or unstable coefficients.
func NewKWeight(sampleRate int) (*KWeight, error) {
	if sampleRate < MinSampleRate || sampleRate > MaxSampleRate {
		return nil, fmt.Errorf("unsupported sample rate %d Hz (want %d..%d)",
			sampleRate, MinSampleRate, MaxSampleRate)
	}
	k := &KWeight{}
	k.shelf = highShelf(sampleRate, 1681.974450955533, 3.999843853973347, 0.7071752369554196)
	k.hp = highPass(sampleRate, 38.13547087602444, 0.5003270373238773)
	if !k.shelf.Stable() || !k.hp.Stable() {
		return nil, fmt.Errorf("unstable K-weighting coefficients at %d Hz", sampleRate)
	}
	return k, nil
}

// Process filters one sample through the full K-weighting chain.
func (k *KWeight) Process(x float64) float64 {
	return k.hp.Process(k.shelf.Process(x))
}

// highShelf derives the BS.1770 pre-filter section via bilinear transform
// of the analog shelf prototype (K = tan pre-warping).
func highShelf(sampleRate int, f0, gainDB, q float64) Biquad {
	K := math.Tan(math.Pi * f0 / float64(sampleRate))
	Vh := math.Pow(10, gainDB/20)
	Vb := math.Pow(Vh, 0.4996667741545416)
	a0 := 1 + K/q + K*K
	return Biquad{
		B0: (Vh + Vb*K/q + K*K) / a0,
		B1: 2 * (K*K - Vh) / a0,
		B2: (Vh - Vb*K/q + K*K) / a0,
		A1: 2 * (K*K - 1) / a0,
		A2: (1 - K/q + K*K) / a0,
	}
}

// highPass derives the BS.1770 RLB high-pass section the same way.
func highPass(sampleRate int, f0, q float64) Biquad {
	K := math.Tan(math.Pi * f0 / float64(sampleRate))
	a0 := 1 + K/q + K*K
	return Biquad{
		B0: 1,
		B1: -2,
		B2: 1,
		A1: 2 * (K*K - 1) / a0,
		A2: (1 - K/q + K*K) / a0,
	}
}
