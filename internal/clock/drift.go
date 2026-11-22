package clock

import "math"

// DriftEstimator performs a least-squares fit of wall-clock nanoseconds against
// media ticks and reports the sender clock's relative rate error in ppm.
// Its zero value is not usable; create it with NewDriftEstimator.
type DriftEstimator struct {
	timescale uint32
	n         int64
	sx, sy    float64
	sxx, sxy  float64
}

// NewDriftEstimator creates an estimator for a track clock of the given rate.
func NewDriftEstimator(timescale uint32) (*DriftEstimator, error) {
	if timescale == 0 {
		return nil, ErrZeroTimescale
	}
	return &DriftEstimator{timescale: timescale}, nil
}

// Add records one observation pair: a media tick count and its wall-clock
// arrival time in nanoseconds.
func (d *DriftEstimator) Add(media, wall int64) {
	x := float64(media)
	y := float64(wall)
	d.n++
	d.sx += x
	d.sy += y
	d.sxx += x * x
	d.sxy += x * y
}

// PPM returns the fitted drift and true when at least two distinct
// observations are available. A positive value means the sender clock runs
// slow (media ticks occupy more wall time than nominal); a negative value means
// it runs fast.
func (d *DriftEstimator) PPM() (int64, bool) {
	if d.n < 2 {
		return 0, false
	}
	n := float64(d.n)
	den := n*d.sxx - d.sx*d.sx
	if den == 0 || math.IsNaN(den) || math.IsInf(den, 0) {
		return 0, false
	}
	slope := (n*d.sxy - d.sx*d.sy) / den // nanoseconds per media tick
	if math.IsNaN(slope) || math.IsInf(slope, 0) {
		return 0, false
	}
	ideal := 1e9 / float64(d.timescale)
	ppm := math.Round((slope/ideal - 1) * 1e6)
	if math.IsNaN(ppm) || ppm < -100000 || ppm > 100000 {
		return 0, false
	}
	return int64(ppm), true
}

// Samples returns the number of observations.
func (d *DriftEstimator) Samples() int64 { return d.n }
