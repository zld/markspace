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

// ClockDrift estimates the rate of a candidate media clock relative to a
// reference clock over a set of observations on the shared arrival clock.
//
// Each track's media time is regressed against arrival time. Delivery jitter
// attenuates both slopes by the same factor, so their ratio is preserved: a
// candidate running 120 ppm fast yields a ratio of 1.00012 regardless of
// per-sample noise. This is markedly more stable than pairing individual
// samples, where reference jitter biases the slope toward zero.
type ClockDrift struct {
	// Ratio is candidate rate / reference rate.
	Ratio float64
	// PPM is (Ratio-1)*1e6.
	PPM float64
	// Clamped reports that MaxPPM limited the correction.
	Clamped bool
	// Insufficient reports that a slope could not be estimated.
	Insufficient bool
	// Samples is the number of observations that contributed.
	Samples int
}

// EstimateClockDrift fits the two clocks and returns the relative rate. A
// non-positive fitted reference slope (a clock that does not advance with
// arrival) means the data is unusable and Insufficient is set.
func EstimateClockDrift(reference, candidate []Observation, maxPPM float64) ClockDrift {
	out := ClockDrift{Ratio: 1}
	if maxPPM <= 0 {
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

// binCount bounds the robust regression's resolution. Each bin contributes
// one (median arrival, median media) point, so isolated late, reordered or
// duplicate deliveries cannot lever the slope.
const binCount = 64

// fitSlope returns the slope of media against arrival, in nanoseconds of
// media per nanosecond of arrival. It uses a binned median regression: the
// samples are split into equal-count arrival bins and each bin's median
// arrival and median media form one regression point. With fewer than two
// usable bins, or if every bin shares one arrival, it reports failure.
func fitSlope(obs []Observation) (float64, bool) {
	if len(obs) < 4 {
		return 0, false
	}
	s := append([]Observation(nil), obs...)
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Arrival != s[j].Arrival {
			return s[i].Arrival < s[j].Arrival
		}
		return s[i].Media < s[j].Media
	})
	bins := binCount
	if len(s) < bins {
		bins = len(s)
	}
	pts := make([]Observation, 0, bins)
	for b := 0; b < bins; b++ {
		lo := b * len(s) / bins
		hi := (b + 1) * len(s) / bins
		if hi <= lo {
			continue
		}
		arrivals := make([]int64, 0, hi-lo)
		medias := make([]int64, 0, hi-lo)
		for _, o := range s[lo:hi] {
			arrivals = append(arrivals, int64(o.Arrival))
			medias = append(medias, int64(o.Media))
		}
		sort.Slice(arrivals, func(i, j int) bool { return arrivals[i] < arrivals[j] })
		sort.Slice(medias, func(i, j int) bool { return medias[i] < medias[j] })
		pts = append(pts, Observation{
			Arrival: model.Nanos(arrivals[len(arrivals)/2]),
			Media:   model.Nanos(medias[len(medias)/2]),
		})
	}
	if len(pts) < 2 {
		return 0, false
	}
	x0, y0 := pts[0].Arrival, pts[0].Media
	var sx, sy, sxx, sxy float64
	n := float64(len(pts))
	for _, o := range pts {
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
	if math.IsNaN(slope) || math.IsInf(slope, 0) {
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
