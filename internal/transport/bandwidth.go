package transport

import "time"

// Estimator tracks the input byte rate and derives a reorder-window hint.
// It only tunes the reorder window; queue capacity is fixed by the pipeline
// and never grows with input length.
type Estimator struct {
	MinWindow   int
	MaxWindow   int
	SampleEvery time.Duration

	hint    int
	ema     float64 // bytes/sec exponential moving average
	ref     float64 // reference rate established after warmup
	last    time.Time
	pending int // bytes since last sample

	Samples uint64
	Shifts  uint64 // window adjustments caused by rate changes
}

// NewEstimator builds an estimator with window bounds; 1 <= minW <= maxW.
func NewEstimator(minW, maxW int, sampleEvery time.Duration) *Estimator {
	if minW < 1 {
		minW = 1
	}
	if maxW < minW {
		maxW = minW
	}
	if sampleEvery <= 0 {
		sampleEvery = 100 * time.Millisecond
	}
	return &Estimator{MinWindow: minW, MaxWindow: maxW, hint: minW, SampleEvery: sampleEvery}
}

// Observe records nbytes arriving at time at and returns the current rate
// estimate in bytes/sec.
func (e *Estimator) Observe(nbytes int, at time.Time) float64 {
	if e.last.IsZero() {
		e.last = at
		e.pending = nbytes
		return 0
	}
	e.pending += nbytes
	dt := at.Sub(e.last)
	if dt < e.SampleEvery {
		return e.ema
	}
	rate := float64(e.pending) / dt.Seconds()
	if e.ema == 0 {
		e.ema = rate
	} else {
		e.ema = 0.7*e.ema + 0.3*rate
	}
	e.pending = 0
	e.last = at
	e.Samples++
	if e.Samples >= 2 && e.ref == 0 {
		e.ref = e.ema
	}
	e.adjust()
	return e.ema
}

// adjust moves the window hint when the rate departs from the reference.
func (e *Estimator) adjust() {
	if e.ref <= 0 {
		return
	}
	switch {
	case e.ema < 0.6*e.ref && e.hint < e.MaxWindow:
		e.hint *= 2
		if e.hint > e.MaxWindow {
			e.hint = e.MaxWindow
		}
		e.Shifts++
	case e.ema > 1.8*e.ref && e.hint > e.MinWindow:
		e.hint /= 2
		if e.hint < e.MinWindow {
			e.hint = e.MinWindow
		}
		e.Shifts++
	}
}

// WindowHint returns the current reorder-window hint within bounds.
func (e *Estimator) WindowHint() int { return e.hint }
