// Package report computes quantized media metrics and writes terminal/manifest
// reports.
package report

import (
	"math"

	"mediapack/internal/clock"
)

// PixelMean returns the deterministic mean of off-screen frame bytes. Frame
// bytes are generated as 8-bit luma-like values, so the result is in [0,255].
func PixelMean(frame []byte) float64 {
	if len(frame) == 0 {
		return 0
	}
	var sum uint64
	for _, b := range frame {
		sum += uint64(b)
	}
	return math.Round(float64(sum)/float64(len(frame))*1000) / 1000
}

// PeakDBFS converts the peak absolute normalized amplitude to dBFS.
// Digital zero is reported as the finite floor -96 dBFS for stable JSON.
func PeakDBFS(samples []float64) float64 {
	peak := 0.0
	for _, s := range samples {
		a := math.Abs(s)
		if a > peak {
			peak = a
		}
	}
	if peak <= 1e-10 {
		return -96
	}
	db := 20 * math.Log10(peak)
	return math.Round(db*1000) / 1000
}

// AlignmentErrorMS converts an absolute tick error to milliseconds.
func AlignmentErrorMS(errTicks clock.Ticks) float64 {
	v := math.Abs(errTicks.Milliseconds())
	return math.Round(v*1000) / 1000
}

// AllHex64 reports whether every supplied hash is lowercase 64-digit hex.
func AllHex64(hashes ...string) bool {
	for _, h := range hashes {
		if len(h) != 64 {
			return false
		}
		for _, c := range h {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return false
			}
		}
	}
	return true
}
