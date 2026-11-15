// Package metrics computes RTP arrival jitter and renders run reports.
package metrics

import "time"

// Jitter implements the RFC 3550 interarrival jitter estimator:
//
//	D(i) = (R_i - R_{i-1}) - (S_i - S_{i-1})
//	J(i) = J(i-1) + (|D(i)| - J(i-1)) / 16
//
// with RTP timestamps S in clock-rate units and arrivals R converted to the
// same units. It tolerates timestamp wraparound and non-monotonic arrival
// clocks.
type Jitter struct {
	clockRate   float64
	jitter      float64 // in clock-rate units
	samples     uint64
	lastTS      uint32
	lastArrival time.Time
	initialized bool
}

// NewJitter creates an estimator for the given RTP clock rate (e.g. 90000).
func NewJitter(clockRate uint32) *Jitter {
	return &Jitter{clockRate: float64(clockRate)}
}

// Observe feeds one received packet: its RTP timestamp and local arrival
// time. The first packet only initializes the estimator.
func (j *Jitter) Observe(ts uint32, arrival time.Time) {
	if !j.initialized {
		j.initialized = true
		j.lastTS = ts
		j.lastArrival = arrival
		return
	}
	// uint32 subtraction handles timestamp wraparound correctly.
	tsDelta := float64(ts - j.lastTS)
	arrivalDelta := arrival.Sub(j.lastArrival).Seconds() * j.clockRate
	if arrivalDelta < 0 {
		// Non-monotonic clock sample: clamp instead of feeding a bogus
		// negative transit time into the estimator.
		arrivalDelta = 0
	}
	d := arrivalDelta - tsDelta
	if d < 0 {
		d = -d
	}
	j.jitter += (d - j.jitter) / 16
	j.samples++
	j.lastTS = ts
	j.lastArrival = arrival
}

// Samples returns the number of jitter samples (received packets minus one).
func (j *Jitter) Samples() uint64 { return j.samples }

// Valid reports whether the estimate is meaningful. A single sample is too
// little to characterize the link, so at least two are required.
func (j *Jitter) Valid() bool { return j.samples >= 2 }

// Milliseconds returns the current jitter estimate in milliseconds. It is
// always non-negative and finite; callers should check Valid first.
func (j *Jitter) Milliseconds() float64 {
	return j.jitter / j.clockRate * 1000
}
