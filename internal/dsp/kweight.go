// Package dsp implements the BS.1770 K-weighting pre-filter and a 4x
// oversampling true-peak detector.
package dsp

import "math"

// kFilterSpec holds the analog prototype constants shared by every sample
// rate. They are the values published in ITU-R BS.1770-4.
const (
	stage1Freq   = 1681.974450955533
	stage1GainDB = 3.999843853973347
	stage1Q      = 0.7071752369554196
	stage1PR     = 0.4996667741545416 // exponent of the high-shelf gain
	stage2Freq   = 38.13547087602444
	stage2Q      = 0.5003270373238773
)

// Biquad is a second order IIR section in direct form I. Coefficients are
// normalized so that a0 == 1.
type Biquad struct {
	B0, B1, B2 float64
	A1, A2     float64
}

// Filter is the two cascaded biquads that make up the K-weighting curve.
// Each channel owns its own filter so per-channel state never leaks.
type Filter struct {
	Stage1 Biquad
	Stage2 Biquad

	z1x1, z1x2, z1y1, z1y2 float64
	z2x1, z2x2, z2y1, z2y2 float64
}

// NewKFilter derives K-weighting coefficients for the given sample rate.
// Coefficient derivation is the one described by ITU-R BS.1770-4 and used by
// libebur128: a high shelf at ~1682 Hz followed by a high-pass at ~38 Hz.
func NewKFilter(sampleRate int) *Filter {
	fs := float64(sampleRate)
	f := &Filter{}
	f.Stage1 = designHighShelf(stage1Freq, stage1GainDB, stage1Q, stage1PR, fs)
	f.Stage2 = designHighPass(stage2Freq, stage2Q, fs)
	return f
}

func designHighShelf(f0, gainDB, q, pr, fs float64) Biquad {
	k := math.Tan(math.Pi * f0 / fs)
	vh := math.Pow(10, gainDB/20)
	vb := math.Pow(vh, pr)
	a0 := 1 + k/q + k*k
	return Biquad{
		B0: (vh + vb*k/q + k*k) / a0,
		B1: 2 * (k*k - vh) / a0,
		B2: (vh - vb*k/q + k*k) / a0,
		A1: 2 * (k*k - 1) / a0,
		A2: (1 - k/q + k*k) / a0,
	}
}

func designHighPass(f0, q, fs float64) Biquad {
	k := math.Tan(math.Pi * f0 / fs)
	a0 := 1 + k/q + k*k
	return Biquad{
		B0: 1,
		B1: -2,
		B2: 1,
		A1: 2 * (k*k - 1) / a0,
		A2: (1 - k/q + k*k) / a0,
	}
}

// Reset clears the filter memory. It must be called between files so that a
// previous file's tail cannot bleed into the next measurement.
func (f *Filter) Reset() {
	f.z1x1, f.z1x2, f.z1y1, f.z1y2 = 0, 0, 0, 0
	f.z2x1, f.z2x2, f.z2y1, f.z2y2 = 0, 0, 0, 0
}

// Process filters one sample and returns the K-weighted value.
func (f *Filter) Process(x float64) float64 {
	y1 := f.Stage1.B0*x + f.Stage1.B1*f.z1x1 + f.Stage1.B2*f.z1x2 -
		f.Stage1.A1*f.z1y1 - f.Stage1.A2*f.z1y2
	f.z1x2 = f.z1x1
	f.z1x1 = x
	f.z1y2 = f.z1y1
	f.z1y1 = y1

	y2 := f.Stage2.B0*y1 + f.Stage2.B1*f.z2x1 + f.Stage2.B2*f.z2x2 -
		f.Stage2.A1*f.z2y1 - f.Stage2.A2*f.z2y2
	f.z2x2 = f.z2x1
	f.z2x1 = y1
	f.z2y2 = f.z2y1
	f.z2y1 = y2
	return y2
}
