// Package timeline advances an event-time watermark and manages
// deterministic replay of packets that arrive after it.
package timeline

import "mediaforge/internal/clock"

// Watermark is the latest media time minus the configured reorder delay.
// It never moves backwards.
type Watermark struct {
	delay clock.Rat
	max   clock.Time
	has   bool
}

// NewWatermark creates a watermark with a non-negative delay in seconds.
func NewWatermark(delay clock.Rat) *Watermark {
	if delay.Num < 0 {
		delay = clock.Rat{Num: 0, Den: 1}
	}
	return &Watermark{delay: delay}
}

// Observe advances the watermark with an event time. It reports whether the
// underlying maximum moved.
func (w *Watermark) Observe(t clock.Time) bool {
	if !w.has || t.CmpTime(w.max) > 0 {
		w.max, w.has = t, true
		return true
	}
	return false
}

// Max returns the greatest observed event time.
func (w *Watermark) Max() (clock.Time, bool) { return w.max, w.has }

// Horizon returns the current watermark in seconds.
func (w *Watermark) Horizon() (clock.Rat, bool) {
	if !w.has {
		return clock.Rat{}, false
	}
	h := w.max.Seconds().Sub(w.delay)
	if h.Num < 0 {
		h = clock.Rat{Num: 0, Den: 1}
	}
	return h, true
}

// IsLate reports whether an event time lies before the current horizon.
func (w *Watermark) IsLate(t clock.Time) bool {
	h, ok := w.Horizon()
	if !ok {
		return false
	}
	return t.Seconds().Cmp(h) < 0
}

// SamplesAt converts a rational number of seconds to a sample index.
func SamplesAt(seconds clock.Rat, rate int64) int64 {
	return clock.MulDivRound(seconds.Num, rate, seconds.Den)
}
