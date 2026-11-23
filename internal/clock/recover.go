package clock

import "math"

// SRPoint is one paired observation of an RTP timestamp and a wall-clock
// sender-report NTP time, expressed in nanoseconds since an arbitrary epoch
// (only differences are meaningful).
type SRPoint struct {
	RTP     int64 // unwrapped RTP clock units
	WallNS  int64 // sender-report NTP time in nanoseconds
	Arrival int64 // local receive time in nanoseconds
}

// DriftEstimator fits device clock drift from RTCP Sender Reports paired with
// RTP timestamps using ordinary least squares. Estimation restarts after a
// gap larger than half the 32-bit period so stale points never span a wrap.
type DriftEstimator struct {
	points []SRPoint
	n      int
	sumX   float64
	sumY   float64
	sumXX  float64
	sumXY  float64
}

// NewDriftEstimator constructs an empty estimator.
func NewDriftEstimator() *DriftEstimator {
	return &DriftEstimator{points: make([]SRPoint, 0, 64)}
}

// Add folds one sender-report observation into the regression. Observations
// must be supplied in arrival order. The capacity bound is enforced.
func (d *DriftEstimator) Add(p SRPoint) *Error {
	if len(d.points) >= MaxSRPoints {
		return NewError(ErrOverflow, "sender-report estimator capacity exceeded")
	}
	if len(d.points) > 0 {
		last := d.points[len(d.points)-1]
		if p.WallNS <= last.WallNS || p.RTP <= last.RTP {
			return NewError(ErrBackwards, "sender report is not monotonic")
		}
	}
	d.points = append(d.points, p)
	x := float64(p.WallNS)
	y := float64(p.RTP)
	d.n++
	d.sumX += x
	d.sumY += y
	d.sumXX += x * x
	d.sumXY += x * y
	return nil
}

// PPM returns the estimated device drift in parts per million relative to the
// nominal clock rate, and whether an estimate is available. Positive drift
// means the device clock runs fast.
func (d *DriftEstimator) PPM(rate uint32) (float64, bool) {
	if d.n < 2 || rate == 0 {
		return 0, false
	}
	n := float64(d.n)
	denom := n*d.sumXX - d.sumX*d.sumX
	if denom == 0 {
		return 0, false
	}
	slope := (n*d.sumXY - d.sumX*d.sumY) / denom // RTP units per nanosecond
	nominal := float64(rate) / 1e9
	return (slope/nominal - 1) * 1e6, true
}

// InterceptRTP returns the regression intercept expressed in RTP units.
func (d *DriftEstimator) InterceptRTP() (float64, bool) {
	if d.n < 2 {
		return 0, false
	}
	n := float64(d.n)
	denom := n*d.sumXX - d.sumX*d.sumX
	if denom == 0 {
		return 0, false
	}
	slope := (n*d.sumXY - d.sumX*d.sumY) / denom
	return (d.sumY - slope*d.sumX) / n, true
}

// Len reports how many observations have been ingested.
func (d *DriftEstimator) Len() int { return d.n }

// MediaClock maps extended (unwrapped) RTP timestamps onto a monotonic media
// time line in nanoseconds, correcting device drift from sender reports.
type MediaClock struct {
	Rate     uint32
	firstRTP int64
	started  bool
	driftPPM float64
	slope    float64 // nanoseconds per RTP unit, drift corrected
}

// NewMediaClock constructs a media clock at the given RTP rate.
func NewMediaClock(rate uint32) *MediaClock {
	return &MediaClock{Rate: rate, slope: 1e9 / float64(rate)}
}

// Fit calibrates the clock from a populated drift estimator.
func (m *MediaClock) Fit(d *DriftEstimator) bool {
	ppm, ok := d.PPM(m.Rate)
	if !ok {
		return false
	}
	m.driftPPM = ppm
	// Device slope in RTP units per ns; invert for ns-per-unit mapping.
	deviceSlope := (float64(m.Rate) / 1e9) * (1 + ppm/1e6)
	if deviceSlope <= 0 {
		return false
	}
	m.slope = 1 / deviceSlope
	return true
}

// Anchor sets the origin: the given extended RTP timestamp becomes media
// time zero.
func (m *MediaClock) Anchor(firstRTP int64) {
	m.firstRTP = firstRTP
	m.started = true
}

// ToMediaNS converts an extended RTP timestamp to media time in nanoseconds.
// The output is clamped monotonic: any backwards mapping is reported and the
// previous value is held rather than emitting a negative step.
func (m *MediaClock) ToMediaNS(extended int64) (int64, *Error) {
	if !m.started {
		return 0, NewError(ErrBackwards, "media clock is not anchored")
	}
	ns := float64(extended-m.firstRTP) * m.slope
	v := int64(math.Round(ns))
	if v < 0 {
		return 0, NewError(ErrBackwards, "media time predates anchor")
	}
	if v > MaxDurationMicros*1000 {
		return 0, NewError(ErrOverflow, "media time exceeds duration bound")
	}
	return v, nil
}

// DriftPPM exposes the calibrated drift in parts per million.
func (m *MediaClock) DriftPPM() float64 { return m.driftPPM }
