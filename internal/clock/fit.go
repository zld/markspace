// Package clock provides the shared nanosecond time base used by every stage
// of the pipeline. This file holds the per-track rate fit.
package clock

import "math"

// fitSlope estimates raw nanoseconds per true nanosecond from (nominal, raw)
// pairs using least squares, clamped to the configured drift bound. Samples
// are already offset by the track's first observation, keeping the sums well
// inside float64 precision.
func fitSlope(samples []sample, maxPPM int64) (float64, bool) {
	if len(samples) < 2 {
		return 0, false
	}
	var n, sx, sy, sxx, sxy float64
	for _, s := range samples {
		x := float64(s.nominalNs)
		y := float64(s.rawNs)
		n++
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, false
	}
	slope := (n*sxy - sx*sy) / den
	if math.IsNaN(slope) || math.IsInf(slope, 0) || slope <= 0 {
		return 0, false
	}
	lo := 1 - float64(maxPPM)/1e6
	hi := 1 + float64(maxPPM)/1e6
	if slope < lo {
		slope = lo
	}
	if slope > hi {
		slope = hi
	}
	return slope, true
}
