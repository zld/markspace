package clock

import "math"

// Timeline maps a source tick counter (RTP clock units, MP4 track
// timescale units or manifest milliseconds) onto the unified rational
// second base, optionally applying a drift correction learned from
// multiple observation points.
type Timeline struct {
	// TickRate is ticks per source second (e.g. 90000 for RTP video).
	TickRate uint32
	// StartTick is the expanded tick value treated as time zero.
	StartTick int64
	// DriftPPM is the applied correction in parts per million.
	DriftPPM float64
}

// NewTimeline builds a timeline for the given source tick rate.
func NewTimeline(tickRate uint32) *Timeline {
	if tickRate == 0 {
		panic("clock: zero tick rate")
	}
	return &Timeline{TickRate: tickRate}
}

// SetOrigin anchors time zero at the given expanded tick.
func (t *Timeline) SetOrigin(tick int64) *Timeline {
	t.StartTick = tick
	return t
}

// SetDriftPPM installs a drift correction estimate.
func (t *Timeline) SetDriftPPM(ppm float64) *Timeline {
	t.DriftPPM = ppm
	return t
}

// At converts one expanded tick to uncorrected rational seconds.
func (t *Timeline) At(tick int64) Rat {
	return NewRat(tick-t.StartTick, int64(t.TickRate))
}

// Correct applies the drift estimate. A positive DriftPPM means the
// source clock runs fast (ticks accumulate ahead of real time), so the
// raw elapsed value is divided by (1+ppm/1e6). The result is quantized
// to whole microseconds so repeated runs stay identical.
func (t *Timeline) Correct(tick int64) Rat {
	raw := t.At(tick)
	if t.DriftPPM == 0 {
		return raw
	}
	adjustedMicros := math.Round(raw.Seconds() * 1e6 / (1 + t.DriftPPM/1e6))
	return NewRat(int64(adjustedMicros), 1_000_000)
}

// CorrectionDelta returns corrected-raw seconds as a rational.
func (t *Timeline) CorrectionDelta(tick int64) Rat {
	return t.Correct(tick).Sub(t.At(tick))
}

// Observation pairs a reference (wall/manifest) time with a source tick.
type Observation struct {
	RefSeconds Rat
	Tick       int64
}

// DriftEstimator fits a linear mapping between a trusted reference clock
// and a drifting source using two or more observation points.
type DriftEstimator struct {
	rate uint32
	obs  []Observation
}

// NewDriftEstimator requires the source tick rate.
func NewDriftEstimator(tickRate uint32) *DriftEstimator {
	return &DriftEstimator{rate: tickRate}
}

// Add registers one observation point.
func (d *DriftEstimator) Add(o Observation) {
	d.obs = append(d.obs, o)
}

// EstimatePPM runs an ordinary least-squares fit of source elapsed
// seconds against reference elapsed seconds and returns the drift in PPM
// (positive = source clock runs fast). Fewer than two points (or a
// degenerate interval) yields zero.
func (d *DriftEstimator) EstimatePPM() float64 {
	if len(d.obs) < 2 {
		return 0
	}
	first := d.obs[0]
	var sumX, sumY, sumXY, sumXX float64
	n := float64(len(d.obs))
	for _, o := range d.obs {
		// Regress source elapsed seconds (y) on reference elapsed
		// seconds (x): slope > 1 means the source clock runs fast.
		x := o.RefSeconds.Sub(first.RefSeconds).Seconds()
		y := NewRat(o.Tick-first.Tick, int64(d.rate)).Seconds()
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	denom := n*sumXX - sumX*sumX
	if denom == 0 {
		return 0
	}
	slope := (n*sumXY - sumX*sumY) / denom
	return (slope - 1) * 1e6
}

// Residuals returns, per observation, (reference - corrected source) in
// microseconds: the before/after correction difference record.
func (d *DriftEstimator) Residuals(ppm float64) []int64 {
	out := make([]int64, 0, len(d.obs))
	if len(d.obs) == 0 {
		return out
	}
	tl := NewTimeline(d.rate).SetOrigin(d.obs[0].Tick).SetDriftPPM(ppm)
	base := d.obs[0].RefSeconds
	for _, o := range d.obs {
		corrected := base.Add(tl.Correct(o.Tick))
		residual := o.RefSeconds.Sub(corrected)
		out = append(out, residual.RoundMillis()*1000)
	}
	return out
}
