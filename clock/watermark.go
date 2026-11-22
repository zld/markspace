package clock

import "math"

// Watermark tracks a lagging event-time horizon: any window ending at
// or before the watermark is closed to new data. The watermark never
// retreats.
type Watermark struct {
	// Lag is how far behind the maximum observed event time the
	// watermark trails, in ticks.
	Lag int64

	max     int64
	current int64
	init    bool
}

// NewWatermark creates a Watermark trailing max event time by lag ticks.
func NewWatermark(lag int64) *Watermark {
	return &Watermark{Lag: lag, current: math.MinInt64}
}

// Observe records an event time and returns the (possibly advanced)
// watermark.
func (w *Watermark) Observe(t int64) int64 {
	if !w.init || t > w.max {
		w.max = t
	}
	w.init = true
	if wm := w.max - w.Lag; wm > w.current {
		w.current = wm
	}
	return w.current
}

// Current returns the watermark, or math.MinInt64 before any event.
func (w *Watermark) Current() int64 {
	return w.current
}

// Closed reports whether event time t already falls at or behind the
// watermark, i.e. its window is closed and t is a late arrival.
func (w *Watermark) Closed(t int64) bool {
	return w.init && t <= w.current
}
