// Package timeline owns the shared watermark that every module reads,
// and the deterministic replay that recomputes results when a late event
// lands inside the retained late window.
package timeline

// Watermark is a monotone clock derived from event arrivals. Media whose
// presentation time falls depth behind the watermark is emittable; media
// further back than the late window is finalized.
type Watermark struct {
	depthMs      int64
	lateWindowMs int64
	current      int64
	started      bool
}

// NewWatermark creates a watermark tracker.
func NewWatermark(depthMs, lateWindowMs int64) *Watermark {
	return &Watermark{depthMs: depthMs, lateWindowMs: lateWindowMs}
}

// Advance moves the watermark to at least arrivalMs. It never retreats.
func (w *Watermark) Advance(arrivalMs int64) {
	if !w.started || arrivalMs > w.current {
		w.current = arrivalMs
		w.started = true
	}
}

// Current returns the watermark value.
func (w *Watermark) Current() int64 { return w.current }

// Emittable reports whether an event with the given presentation time
// may leave the jitter buffer at the current watermark.
func (w *Watermark) Emittable(eventMs int64) bool {
	return w.started && eventMs+w.depthMs <= w.current
}

// LateBy reports how far past its emit deadline an event arrived.
// Non-positive means on time.
func (w *Watermark) LateBy(eventMs int64) int64 {
	if !w.started {
		return 0
	}
	return w.current - (eventMs + w.depthMs)
}

// InLateWindow reports whether an event arriving late may still trigger
// a replay (true) or must be counted as dropped (false).
func (w *Watermark) InLateWindow(eventMs int64) bool {
	late := w.LateBy(eventMs)
	return late > 0 && late <= w.lateWindowMs
}

// FinalizedAt returns the watermark time at which a presentation time
// leaves the late window and its loss becomes permanent.
func (w *Watermark) FinalizedAt(eventMs int64) int64 {
	return eventMs + w.depthMs + w.lateWindowMs
}
