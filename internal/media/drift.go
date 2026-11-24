package media

import "math"

// Drift is the result of fitting a remote clock (tick samples observed at
// local nanosecond instants) against the local clock. Slope is dimensionless
// (remote-nanos per local-nano, 1.0 means no drift); InterceptNanos is the
// fitted offset at local time zero. R2 quantifies fit quality so callers can
// tell a constant offset (slope 1, large intercept) from real skew.
type Drift struct {
	Slope          float64
	InterceptNanos float64
	R2             float64
	// PPM is (Slope-1)*1e6, parts per million of skew.
	PPM float64
	// Samples is the number of points used for the fit.
	Samples int
}

// EstimateDrift fits remote clock readings (in nanoseconds on the unified
// timeline) against the local nanosecond instants at which they were
// observed, using ordinary least squares. Fewer than two samples, or a
// degenerate x axis, yields a zero-result with R2 set to 0 so the caller can
// mark the estimate indeterminate rather than report a fabricated slope.
func EstimateDrift(localNanos, remoteNanos []int64) Drift {
	n := len(localNanos)
	if n < 2 {
		return Drift{Slope: math.NaN(), R2: math.NaN(), Samples: n}
	}
	var sx, sy, sxx, sxy, syy float64
	for i := 0; i < n; i++ {
		x := float64(localNanos[i])
		y := float64(remoteNanos[i])
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
		syy += y * y
	}
	fn := float64(n)
	denom := fn*sxx - sx*sx
	if denom == 0 {
		return Drift{Slope: math.NaN(), R2: math.NaN(), Samples: n}
	}
	slope := (fn*sxy - sx*sy) / denom
	intercept := (sy - slope*sx) / fn
	// Coefficient of determination.
	meanY := sy / fn
	var ssTot, ssRes float64
	for i := 0; i < n; i++ {
		x := float64(localNanos[i])
		y := float64(remoteNanos[i])
		dy := y - meanY
		ssTot += dy * dy
		res := y - (slope*x + intercept)
		ssRes += res * res
	}
	r2 := 1.0
	if ssTot > 0 {
		r2 = 1 - ssRes/ssTot
	}
	return Drift{
		Slope:          slope,
		InterceptNanos: intercept,
		R2:             r2,
		PPM:            (slope - 1) * 1e6,
		Samples:        n,
	}
}

// CorrectNanos applies a drift model to a remote timestamp and returns the
// local-timeline estimate. A NaN or zero slope (indeterminate/zero-value
// model) leaves the input unchanged.
func (d Drift) CorrectNanos(remoteNanos int64) int64 {
	if math.IsNaN(d.Slope) || d.Slope == 0 {
		return remoteNanos
	}
	return int64(math.Round((float64(remoteNanos) - d.InterceptNanos) / d.Slope))
}
