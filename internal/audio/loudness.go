package audio

import (
	"errors"
	"math"
)

// Package-level sentinel errors.
var (
	ErrBadRate   = errors.New("sample rate must be positive")
	ErrBadTarget = errors.New("target LUFS must be negative")
	ErrNoSamples = errors.New("cannot measure empty PCM buffer")
)

// FullScale is the int16 full-scale amplitude as float.
const FullScale = 32768.0

// LoudnessStats summarizes one processed PCM buffer.
type LoudnessStats struct {
	RMS          float64
	MeasuredLUFS float64
	GainDB       float64
	PeakBefore   float64 // dBFS of the loudest sample before the limiter
	PeakAfter    float64 // dBFS after gain and limiting
	Clipped      int     // samples the limiter had to pull back
}

// dbFS converts a linear amplitude (0..1 relative to full scale) to dBFS.
func dbFS(amp float64) float64 {
	if amp <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(amp)
}

// MeasureLUFS returns the integrated loudness of mono/interleaved PCM using
// an RMS-based K-weighting-free estimate (calibration offset included).
func MeasureLUFS(samples []int16) (float64, error) {
	if len(samples) == 0 {
		return 0, ErrNoSamples
	}
	var sumSq float64
	for _, s := range samples {
		v := float64(s) / FullScale
		sumSq += v * v
	}
	rms := math.Sqrt(sumSq / float64(len(samples)))
	if rms == 0 {
		return math.Inf(-1), nil // digital silence
	}
	// -0.691 dB is the standard RMS-to-LUFS calibration offset.
	return dbFS(rms) - 0.691, nil
}

// ApplyLoudness brings the buffer to targetLUFS with a hard peak limiter at
// 0 dBFS. Silent input keeps gain at 0 dB instead of producing infinite gain;
// any sample that would clip is limited and reported with before/after peaks.
func ApplyLoudness(samples []int16, targetLUFS float64) (LoudnessStats, error) {
	st := LoudnessStats{PeakBefore: math.Inf(-1), PeakAfter: math.Inf(-1)}
	if len(samples) == 0 {
		return st, ErrNoSamples
	}
	if targetLUFS >= 0 {
		return st, ErrBadTarget
	}
	measured, err := MeasureLUFS(samples)
	if err != nil {
		return st, err
	}
	st.MeasuredLUFS = measured

	var sumSq, peakBefore float64
	for _, s := range samples {
		v := float64(s) / FullScale
		sumSq += v * v
		if a := math.Abs(v); a > peakBefore {
			peakBefore = a
		}
	}
	st.RMS = math.Sqrt(sumSq / float64(len(samples)))
	st.PeakBefore = dbFS(peakBefore)

	gainDB := 0.0
	if !math.IsInf(measured, 0) {
		gainDB = targetLUFS - measured
	}
	st.GainDB = gainDB
	gain := math.Pow(10, gainDB/20.0)

	var peakAfter float64
	for i, s := range samples {
		v := float64(s) * gain
		if v > math.MaxInt16 {
			v = math.MaxInt16
			st.Clipped++
		} else if v < math.MinInt16 {
			v = math.MinInt16
			st.Clipped++
		}
		samples[i] = int16(math.Round(v))
		if a := math.Abs(v) / FullScale; a > peakAfter {
			peakAfter = a
		}
	}
	st.PeakAfter = dbFS(peakAfter)
	return st, nil
}

// PeakDBFS returns the peak level in dBFS without modifying the buffer.
func PeakDBFS(samples []int16) (float64, error) {
	if len(samples) == 0 {
		return 0, ErrNoSamples
	}
	var peak float64
	for _, s := range samples {
		if a := math.Abs(float64(s)) / FullScale; a > peak {
			peak = a
		}
	}
	return dbFS(peak), nil
}
