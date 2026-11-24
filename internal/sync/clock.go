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
	// Samples is the number of observations that contributed.
	Samples int `json:"samples"`
}

// EstimateClockDrift compares two tracks observed against the same capture
// arrival clock.
//
// Each track's media time is regressed against arrival time. Delivery jitter
// is independent of the media clock and, over a capture of many samples,
// averages to the same attenuation for both tracks; their slope ratio is
// therefore the relative media clock rate, while a direct audio-vs-video
// regression would inherit the reference track's jitter as bias. Both
// regressions use fast and robust trimmed least squares so isolated late,
// reordered or duplicated deliveries cannot lever the result.
func EstimateClockDrift(reference, candidate []Observation, cfg Config) ClockDrift {
	out := ClockDrift{Ratio: 1}
	maxPPM := cfg.MaxPPM
	if maxPPM <= 0 {
		out.Insufficient = true
		return out
	}
	min := cfg.MinSamples
	if min < 8 {
		min = 8
	}
	if len(reference) < min || len(candidate) < min {
		out.Insufficient = true
		return out
	}
	sr, okr := fitSlope(reference)
	sc, okc := fitSlope(candidate)
	if !okr || !okc || sr <= 0 || sc <= 0 {
		out.Insufficient = true
		return out
	}
	ratio := sc / sr
	out.Samples = len(reference) + len(candidate)
	out.Ratio = ratio
	out.PPM = (ratio - 1) * 1e6
	limit := maxPPM / 1e6
	if ratio > 1+limit {
		out.Ratio, out.PPM, out.Clamped = 1+limit, maxPPM, true
	} else if ratio < 1-limit {
		out.Ratio, out.PPM, out.Clamped = 1-limit, -maxPPM, true
	}
	return out
}

// trimFraction is the share of samples dropped from each end of the
// arrival-ordered set before fitting, so outliers cannot steer the slope.
const trimFraction = 10

// fitSlope regresses media against arrival after trimming the earliest and
// latest tenth of samples and dropping observations whose residual exceeds
// three robust standard deviations.
func fitSlope(obs []Observation) (float64, bool) {
	if len(obs) < 20 {
		return 0, false
	}
	s := append([]Observation(nil), obs...)
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Arrival != s[j].Arrival {
			return s[i].Arrival < s[j].Arrival
		}
		return s[i].Media < s[j].Media
	})
	cut := len(s) / trimFraction
	s = s[cut : len(s)-cut]
	slope, ok := olsSlope(s)
	if !ok {
		return 0, false
	}
	// Residual-based trimming: drop points more than three robust sigmas
	// from the first fit and refit once.
	intercept := float64(s[0].Media) - slope*float64(s[0].Arrival)
	var residuals []float64
	for _, o := range s {
		r := float64(o.Media) - (intercept + slope*float64(o.Arrival))
		residuals = append(residuals, r)
	}
	med := medianFloat(residuals)
	var absDev []float64
	for _, r := range residuals {
		absDev = append(absDev, math.Abs(r-med))
	}
	mad := medianFloat(absDev)
	if mad == 0 {
		return slope, true
	}
	limit := 3 * 1.4826 * mad
	kept := make([]Observation, 0, len(s))
	for i, o := range s {
		if math.Abs(residuals[i]-med) <= limit {
			kept = append(kept, o)
		}
	}
	if len(kept) < len(s)/2 || len(kept) < 8 {
		return slope, true
	}
	if refined, ok := olsSlope(kept); ok {
		return refined, true
	}
	return slope, true
}

func olsSlope(s []Observation) (float64, bool) {
	n := float64(len(s))
	if n < 8 {
		return 0, false
	}
	x0, y0 := s[0].Arrival, s[0].Media
	var sx, sy, sxx, sxy float64
	for _, o := range s {
		x := float64(o.Arrival - x0)
		y := float64(o.Media - y0)
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
	return slope, true
}

func medianFloat(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
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
