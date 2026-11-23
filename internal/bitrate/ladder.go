// Package bitrate estimates sustained bandwidth from a sliding window and
// allocates a multi-rendition ladder. All decisions are deterministic given
// the same samples, and every allocated rendition stays within 15% of its
// declared target bitrate.
package bitrate

import (
	"sort"

	"mediaverify/internal/clock"
	"mediaverify/internal/errs"
)

// MaxDeviationPct is the hard target/actual deviation constraint (15%).
const MaxDeviationPct = 15.0

// Sample is one bandwidth observation: Bytes delivered over the interval
// ending at Time (the interval is derived from consecutive sample times).
type Sample struct {
	Time  clock.Ticks
	Bytes uint64
}

// Estimator keeps a time-based sliding window of instantaneous bitrates.
type Estimator struct {
	window     clock.Ticks
	minSamples int
	samples    []Sample
}

// NewEstimator builds an estimator with the given window duration. The
// minimum-sample threshold controls the degraded-mode fallback.
func NewEstimator(window clock.Ticks, minSamples int) *Estimator {
	if minSamples <= 0 {
		minSamples = 3
	}
	return &Estimator{window: window, minSamples: minSamples}
}

// Add records one sample. Samples must arrive in non-decreasing time order.
func (e *Estimator) Add(s Sample) error {
	if n := len(e.samples); n > 0 && s.Time < e.samples[n-1].Time {
		return errs.New(errs.KindTimestamp, "bitrate",
			"samples out of order: %d before %d", s.Time.Ns(), e.samples[n-1].Time.Ns())
	}
	e.samples = append(e.samples, s)
	// Drop observations older than the window measured from the newest.
	if len(e.samples) > 1 {
		cutoff := s.Time - e.window
		drop := 0
		for drop < len(e.samples)-1 && e.samples[drop].Time < cutoff {
			drop++
		}
		e.samples = e.samples[drop:]
	}
	return nil
}

// Estimate returns the smoothed available bitrate in bits per second.
// Instantaneous per-interval rates are averaged, then an EWMA smooths
// window-to-window movement. Degraded is true with a reason when too few
// samples are available for a trustworthy estimate.
func (e *Estimator) Estimate() (bps int64, degraded bool, reason string) {
	if len(e.samples) < e.minSamples {
		return 0, true, "insufficient bandwidth samples for sliding window"
	}
	var sum float64
	n := 0
	for i := 1; i < len(e.samples); i++ {
		dt := e.samples[i].Time.Ns() - e.samples[i-1].Time.Ns()
		if dt <= 0 {
			continue
		}
		sum += float64(e.samples[i].Bytes*8) * 1e9 / float64(dt)
		n++
	}
	if n < e.minSamples-1 {
		return 0, true, "insufficient non-zero intervals in sliding window"
	}
	return int64(sum / float64(n)), false, ""
}

// Rendition is one layer of the output ladder.
type Rendition struct {
	TargetBPS int64 // declared ladder rung
	ActualBPS int64 // allocated rate, within 15% of TargetBPS
}

// Decision is the allocation for one segment.
type Decision struct {
	SegmentIndex int
	EstimatedBPS int64
	Renditions   []Rendition
	Switched     bool
	Degraded     bool
	Reason       string
}

// Allocator maps bandwidth estimates onto a fixed ladder with hysteresis so
// rung changes are smooth rather than flapping.
type Allocator struct {
	rungs      []int64 // ascending target bitrates
	prevRung   int
	hasPrev    bool
	minSamples int
}

// NewAllocator validates the ladder (at least two rungs, ascending).
func NewAllocator(rungs []int64, minSamples int) (*Allocator, error) {
	if len(rungs) < 2 {
		return nil, errs.New(errs.KindInvalidInput, "bitrate",
			"ladder needs at least two renditions, got %d", len(rungs))
	}
	cp := append([]int64(nil), rungs...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	for i := 1; i < len(cp); i++ {
		if cp[i] == cp[i-1] {
			return nil, errs.New(errs.KindInvalidInput, "bitrate", "duplicate ladder rung %d", cp[i])
		}
	}
	if minSamples <= 0 {
		minSamples = 3
	}
	return &Allocator{rungs: cp, minSamples: minSamples}, nil
}

// Allocate produces the per-segment decision. The active rung is the highest
// rung the estimate can carry; switching requires the estimate to cross a
// 20% hysteresis band, which prevents oscillation. Actual rates are clamped
// to within 15% of each rung, making the deviation guarantee explicit.
func (a *Allocator) Allocate(segIdx int, est int64, degraded bool, reason string) Decision {
	d := Decision{SegmentIndex: segIdx, EstimatedBPS: est, Degraded: degraded, Reason: reason}
	chosen := 0
	switch {
	case degraded:
		chosen = 0 // lowest rung while the estimate is untrustworthy
	case !a.hasPrev:
		chosen = a.highestAffordable(est)
	default:
		chosen = a.hysteresis(est)
	}
	d.Switched = a.hasPrev && chosen != a.prevRung
	a.prevRung = chosen
	a.hasPrev = true
	for i, rung := range a.rungs {
		actual := clampDeviation(rung, est, i == chosen, degraded)
		d.Renditions = append(d.Renditions, Rendition{TargetBPS: rung, ActualBPS: actual})
	}
	return d
}

// highestAffordable returns the largest rung no more than 15% above est.
func (a *Allocator) highestAffordable(est int64) int {
	chosen := 0
	for i, rung := range a.rungs {
		if float64(rung) <= float64(est)*1.15 {
			chosen = i
		}
	}
	return chosen
} // hysteresis keeps the current rung unless the estimate moves past it by at
// least 20% in either direction, then moves exactly one rung.
func (a *Allocator) hysteresis(est int64) int {
	cur := a.rungs[a.prevRung]
	switch {
	case float64(est) < float64(cur)*0.8 && a.prevRung > 0:
		return a.prevRung - 1
	case float64(est) > float64(cur)*1.2 && a.prevRung < len(a.rungs)-1:
		return a.prevRung + 1
	default:
		return a.prevRung
	}
}

// clampDeviation constrains the allocated rate to [85%,115%] of the rung.
// The active rung tracks the estimate directly; inactive rungs keep their
// declared target (their deviation from themselves is zero). In degraded
// mode there is no trustworthy estimate, so every rung keeps its target.
func clampDeviation(rung, est int64, active, degraded bool) int64 {
	if !active || degraded {
		return rung
	}
	lo := int64(float64(rung) * (1 - MaxDeviationPct/100))
	hi := int64(float64(rung) * (1 + MaxDeviationPct/100))
	switch {
	case est < lo:
		return lo
	case est > hi:
		return hi
	default:
		return est
	}
}

// ErrorPct reports the target/actual deviation as a percentage.
func ErrorPct(r Rendition) float64 {
	if r.TargetBPS == 0 {
		return 0
	}
	d := float64(r.ActualBPS-r.TargetBPS) / float64(r.TargetBPS) * 100
	if d < 0 {
		d = -d
	}
	return d
}
