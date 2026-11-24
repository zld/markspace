package sync

import (
	"math"
	"sort"

	"mediasync/internal/model"
)

// Observation is one sample of a media clock: the media timestamp that was
// observed when the capture host's clock read Arrival.
type Observation struct {
	Arrival model.Nanos
	Media   model.Nanos
}

// ClockDrift is an estimate of a candidate media clock's rate relative to a
// reference media clock.
type ClockDrift struct {
	// Ratio is candidate rate / reference rate.
	Ratio float64 `json:"ratio"`
	// PPM is (Ratio-1)*1e6.
	PPM float64 `json:"ppm"`
	// Clamped reports that MaxPPM limited the correction.
	Clamped bool `json:"clamped"`
	// Insufficient reports that a usable slope could not be estimated.
	Insufficient bool `json:"insufficient"`
	// Bins is the number of arrival bins that contributed.
	Bins int `json:"bins"`
}

// BinCount bounds the resolution of the arrival-binned estimator.
const BinCount = 64

// EstimateClockDrift compares two tracks observed against the same capture
// arrival clock.
//
// Both sample sets are split into the same arrival-time bins. Within each bin
// the median video media time and the median audio media time are taken over
// the same wall-clock interval, so per-packet delivery delay contributes the
// same median offset to both and cancels in the slope. The slope of audio
// media against video media across bins is therefore the relative media clock
// rate, free of the errors-in-variables bias that separate regressions would
// introduce. Medians make the estimate robust to isolated late, reordered or
// duplicated deliveries.
func EstimateClockDrift(reference, candidate []Observation, cfg Config) ClockDrift {
	out := ClockDrift{Ratio: 1}
	maxPPM := cfg.MaxPPM
	bins := BinCount
	if cfg.Window > 0 && cfg.Window < bins {
		bins = cfg.Window
	}
	if maxPPM <= 0 || len(reference) < bins/2 || len(candidate) < bins/2 {
		out.Insufficient = true
		return out
	}

	vx, ax, ok := binnedMedians(reference, candidate, bins)
	if !ok || len(vx) < 3 {
		out.Insufficient = true
		return out
	}
	ratio, ok := slopeRatio(vx, ax)
	if !ok {
		out.Insufficient = true
		return out
	}
	out.Bins = len(vx)
	out.Ratio = ratio
	out.PPM = (ratio - 1) * 1e6
	limit := maxPPM / 1e6
	if ratio > 1+limit {
		out.Ratio = 1 + limit
		out.PPM = maxPPM
		out.Clamped = true
	} else if ratio < 1-limit {
		out.Ratio = 1 - limit
		out.PPM = -maxPPM
		out.Clamped = true
	}
	return out
}

// binnedMedians splits both sets over the shared arrival range and returns the
// per-bin median reference time and median candidate time. Bins where either
// side is empty are skipped.
func binnedMedians(reference, candidate []Observation, bins int) ([]model.Nanos, []model.Nanos, bool) {
	lo, hi, ok := arrivalRange(reference, candidate)
	if !ok || hi <= lo {
		return nil, nil, false
	}
	refBins := make([][]model.Nanos, bins)
	candBins := make([][]model.Nanos, bins)
	fill := func(obs []Observation, dst [][]model.Nanos) {
		for _, o := range obs {
			idx := int(int64(bins) * int64(o.Arrival-lo) / int64(hi-lo))
			if idx < 0 {
				idx = 0
			} else if idx >= bins {
				idx = bins - 1
			}
			dst[idx] = append(dst[idx], o.Media)
		}
	}
	fill(reference, refBins)
	fill(candidate, candBins)
	var vx, ax []model.Nanos
	for i := 0; i < bins; i++ {
		if len(refBins[i]) == 0 || len(candBins[i]) == 0 {
			continue
		}
		vx = append(vx, median(refBins[i]))
		ax = append(ax, median(candBins[i]))
	}
	return vx, ax, true
}

func arrivalRange(sets ...[]Observation) (model.Nanos, model.Nanos, bool) {
	lo := model.Nanos(math.MaxInt64)
	hi := model.Nanos(math.MinInt64)
	found := false
	for _, s := range sets {
		for _, o := range s {
			if o.Arrival < lo {
				lo = o.Arrival
			}
			if o.Arrival > hi {
				hi = o.Arrival
			}
			found = true
		}
	}
	return lo, hi, found
}

func median(v []model.Nanos) model.Nanos {
	if len(v) == 0 {
		return 0
	}
	s := append([]model.Nanos(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

// slopeRatio fits y = ratio*x through the origin with an intercept, i.e. an
// ordinary least squares slope, and rejects degenerate input.
func slopeRatio(x, y []model.Nanos) (float64, bool) {
	n := float64(len(x))
	if n < 3 || float64(len(y)) != n {
		return 0, false
	}
	x0, y0 := x[0], y[0]
	var sx, sy, sxx, sxy float64
	for i := range x {
		dx := float64(x[i] - x0)
		dy := float64(y[i] - y0)
		sx += dx
		sy += dy
		sxx += dx * dx
		sxy += dx * dy
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, false
	}
	slope := (n*sxy - sx*sy) / den
	if math.IsNaN(slope) || math.IsInf(slope, 0) || slope <= 0 {
		return 0, false
	}
	return slope, true
}

// ApplyClockDrift maps a raw candidate timestamp onto the reference rate,
// anchored at the candidate's first observation so the constant start offset
// is untouched. Only the rate is corrected.
func ApplyClockDrift(raw, anchor model.Nanos, ratio float64) model.Nanos {
	if ratio == 0 || ratio == 1 {
		return raw
	}
	return anchor + model.Nanos(math.Round(float64(raw-anchor)/ratio))
}
