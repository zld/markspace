package timeline

// Watermark tracks the highest event time already accepted on one ordered
// media stream. An event at or below the watermark is late by definition.
type Watermark struct {
	level uint64
	set   bool
	// LateCount counts observations at or below the level.
	LateCount int
	// AdvanceCount counts observations that moved the level forward.
	AdvanceCount int
}

// Observe feeds one event time. It returns true when the observation is
// late (its time is at or below the current level) and advances the level
// otherwise. Equal times are considered late, not advances: only strictly
// greater times move the watermark.
func (w *Watermark) Observe(t uint64) (late bool) {
	if !w.set {
		w.set = true
		w.level = t
		w.AdvanceCount++
		return false
	}
	if t <= w.level {
		w.LateCount++
		return true
	}
	w.level = t
	w.AdvanceCount++
	return false
}

// Level returns the current watermark level.
func (w *Watermark) Level() (uint64, bool) { return w.level, w.set }

// Reset restores the initial state.
func (w *Watermark) Reset() { *w = Watermark{} }

// LateDecision describes why an event was classified late and which
// previously accepted time range must be recomputed.
type LateDecision struct {
	TrackID    uint32
	EventTime  uint64
	Watermark  uint64
	// AffectedStart is the earliest accepted event time at or after the
	// late event's time; recomputation covers [AffectedStart, Watermark].
	AffectedStart uint64
	// AffectedEnd is the watermark level at the moment of detection.
	AffectedEnd uint64
}

// Engine is the per-track watermark plus the bounded history needed to
// identify the recomputation interval. History keeps only accepted event
// times, which is O(events-in-window), never O(total bytes).
type Engine struct {
	wm       Watermark
	trackID  uint32
	accepted []uint64 // sorted ascending by observation order
	late     []LateDecision
}

// NewEngine builds an Engine for one track.
func NewEngine(trackID uint32) *Engine {
	return &Engine{trackID: trackID}
}

// Push classifies an event. A late event does not move the watermark and
// yields a LateDecision spanning the accepted interval it intersects.
func (e *Engine) Push(eventTime uint64) (late bool, decision *LateDecision) {
	if e.wm.Observe(eventTime) {
		level, _ := e.wm.Level()
		d := &LateDecision{
			TrackID:   e.trackID,
			EventTime: eventTime,
			Watermark: level,
		}
		d.AffectedStart = e.lowerBound(eventTime)
		d.AffectedEnd = level
		e.late = append(e.late, *d)
		return true, d
	}
	e.accepted = append(e.accepted, eventTime)
	return false, nil
}

// lowerBound returns the smallest accepted time >= t, or the watermark
// when every accepted time is smaller.
func (e *Engine) lowerBound(t uint64) uint64 {
	for _, v := range e.accepted {
		if v >= t {
			return v
		}
	}
	level, _ := e.wm.Level()
	return level
}

// LateDecisions returns a copy of every late decision recorded so far.
func (e *Engine) LateDecisions() []LateDecision {
	out := make([]LateDecision, len(e.late))
	copy(out, e.late)
	return out
}

// Level exposes the underlying watermark level.
func (e *Engine) Level() (uint64, bool) { return e.wm.Level() }

// AcceptedTimes returns a copy of accepted event times.
func (e *Engine) AcceptedTimes() []uint64 {
	out := make([]uint64, len(e.accepted))
	copy(out, e.accepted)
	return out
}
