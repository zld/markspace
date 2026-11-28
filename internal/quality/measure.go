// Package quality owns objective quality measurement and constraint-driven
// configuration selection. It never fabricates a metric: when a stream cannot
// be measured, the result is explicitly marked unmeasurable.
package quality

import "math"

// CodecVersion identifies the deterministic encoder used for rate/quality
// measurement. It participates in the configuration digest.
const CodecVersion = "alab-raw-v1"

// PeakSignal is the 8-bit luma peak used by PSNR.
const PeakSignal = 255.0

// Stats is the measured outcome of encoding one frame.
type Stats struct {
	Bytes  int
	SSE    int64
	Pixels int64
}

// Merge adds another frame's statistics.
func (s *Stats) Merge(o Stats) {
	s.Bytes += o.Bytes
	s.SSE += o.SSE
	s.Pixels += o.Pixels
}

// PSNRFromSSE converts accumulated squared error into peak PSNR. Identical
// frames have unbounded PSNR, reported as +Inf and flagged measurable.
func PSNRFromSSE(sse, pixels int64) (float64, bool) {
	if pixels <= 0 {
		return 0, false
	}
	if sse == 0 {
		return math.Inf(1), true
	}
	mse := float64(sse) / float64(pixels)
	return 10 * math.Log10(PeakSignal*PeakSignal/mse), true
}

// PSNRBetween measures PSNR between two equally sized planes.
func PSNRBetween(ref, got []byte) (float64, bool) {
	if len(ref) == 0 || len(ref) != len(got) {
		return 0, false
	}
	var sse int64
	for i := range ref {
		d := int64(ref[i]) - int64(got[i])
		sse += d * d
	}
	return PSNRFromSSE(sse, int64(len(ref)))
}

// BitrateErrorPct is the signed relative error of actual against target.
func BitrateErrorPct(actual, target int64) float64 {
	if target <= 0 {
		return 0
	}
	return (float64(actual) - float64(target)) / float64(target) * 100
}

// DriftMS converts an absolute nanosecond difference into milliseconds.
func DriftMS(a, b int64) float64 {
	d := a - b
	if d < 0 {
		d = -d
	}
	return float64(d) / 1e6
}
