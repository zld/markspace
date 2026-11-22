package clock

import (
	"math"

	"mediasync/internal/mederr"
)

// Anchor pairs a noisy observed instant (ns) with the known reference
// (true) instant of the same programme moment.
type Anchor struct {
	ObsNS  int64
	TrueNS int64
}

// DriftModel is a least-squares mapping trueNS = Slope*obsNS + Intercept
// fitted from anchors. The float64 fit is deterministic for a fixed input
// sequence; the corrected values are rounded to whole nanoseconds.
type DriftModel struct {
	Slope     float64
	Intercept float64
	Anchors   int
}

// FitDrift requires at least two anchors; a single anchor can only define
// an offset and zero/one anchor cannot estimate a rate.
func FitDrift(anchors []Anchor) (*DriftModel, error) {
	if len(anchors) < 2 {
		return nil, mederr.New(mederr.CodeInternal,
			"drift fit needs at least two anchors")
	}
	var sx, sy, sxx, sxy float64
	for _, a := range anchors {
		x := float64(a.ObsNS)
		y := float64(a.TrueNS)
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	n := float64(len(anchors))
	den := n*sxx - sx*sx
	if math.Abs(den) < 1.0 {
		return nil, mederr.New(mederr.CodeArithmeticOverflow,
			"degenerate anchors: drift rate not identifiable")
	}
	slope := (n*sxy - sx*sy) / den
	intercept := (sy - slope*sx) / n
	if math.IsNaN(slope) || math.IsInf(slope, 0) ||
		math.IsNaN(intercept) || math.IsInf(intercept, 0) {
		return nil, mederr.New(mederr.CodeArithmeticOverflow,
			"drift fit produced non-finite coefficients")
	}
	return &DriftModel{Slope: slope, Intercept: intercept, Anchors: len(anchors)}, nil
}

// Correct maps one observed instant onto the reference time base.
func (m *DriftModel) Correct(obsNS int64) (int64, error) {
	v := m.Slope*float64(obsNS) + m.Intercept
	if math.IsNaN(v) || math.IsInf(v, 0) || v > float64(math.MaxInt64) ||
		v < float64(math.MinInt64) {
		return 0, mederr.New(mederr.CodeArithmeticOverflow,
			"drift correction overflows int64 nanoseconds")
	}
	return int64(math.Round(v)), nil
}

// EnforceMonotonic repairs a nanosecond instant stream:
//   - duplicate consecutive instants are nudged forward by 1 ns (VFR frame
//     ordering preserved, never re-gridded to an even frame rate);
//   - genuine backwards steps are left untouched and reported as
//     discontinuity indices — callers must annotate them explicitly.
//
// It returns the adjusted stream, the nudged indices and the discontinuity
// indices.
func EnforceMonotonic(ns []int64) (fixed []int64, duplicates []int, disc []int) {
	fixed = append([]int64(nil), ns...)
	for i := 1; i < len(fixed); i++ {
		switch {
		case fixed[i] == fixed[i-1]:
			v, err := add64(fixed[i], 1)
			if err != nil {
				// Saturate: keep the duplicate rather than overflow.
				v = fixed[i]
			}
			fixed[i] = v
			duplicates = append(duplicates, i)
		case fixed[i] < fixed[i-1]:
			disc = append(disc, i)
		}
	}
	return fixed, duplicates, disc
}
