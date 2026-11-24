// Package timeline maintains an event-time watermark and recomputes affected
// intervals deterministically when late data arrives inside a fixed window.
//
// The engine never depends on arrival order for its final state: the same
// event set, in any order, yields the same interval summaries and the same
// timeline hash. Events beyond the window are rejected with a structured
// LATE_REJECTED error rather than silently dropped.
//
// Memory is bounded: once the watermark moves more than one late window past
// an interval, that interval can no longer change, so it is folded into a
// resumable hash prefix and its backing events are dropped. Only events inside
// the current window remain in memory, regardless of total stream length.
package timeline

import (
	"sort"

	"mediad/internal/model"
)

// Event is one timestamped piece of media state. Payload is opaque; the
// engine hashes the raw payload bytes so recomputation stays reproducible.
type Event struct {
	ID      string
	TimeNS  int64 // event time on the shared nanosecond clock
	Track   int
	Payload []byte
}

// Interval is one finalized bucket between consecutive event times. Its
// summary is derived solely from the events inside it, never from order.
type Interval struct {
	StartNS     int64  `json:"start_ns"`
	EndNS       int64  `json:"end_ns"`
	Count       int    `json:"count"`
	PayloadHash uint64 `json:"payload_hash"`
}

// EngineConfig parameterizes the recompute policy.
type EngineConfig struct {
	// LateWindowNS is how far behind the watermark an event may still arrive.
	LateWindowNS int64
}

// Engine is the stateful watermark + recompute machine.
type Engine struct {
	cfg       EngineConfig
	watermark int64
	started   bool

	// events retains only events still capable of being affected by an
	// in-window late arrival (TimeNS > watermark - window).
	events []Event
	// active are the intervals rebuilt from events on every recompute.
	active []Interval
	// frozenCount is the number of finalized intervals folded into prefixHash.
	frozenCount int
	// prefixHash is the resumable FNV state over every frozen interval.
	prefixHash uint64

	recomputed int
	rejected   int
}

// NewEngine validates the config and returns an engine.
func NewEngine(cfg EngineConfig) (*Engine, error) {
	if cfg.LateWindowNS <= 0 {
		return nil, model.New(model.ErrLateRejected, "late window must be positive, got %d", cfg.LateWindowNS)
	}
	return &Engine{cfg: cfg, prefixHash: fnvOffsetBasis64}, nil
}

// Push accepts one event. A newer event raises the watermark; an event within
// the window behind it triggers recompute; an event beyond it is rejected.
func (e *Engine) Push(ev Event) error {
	if !e.started {
		e.started = true
		e.watermark = ev.TimeNS
		e.events = append(e.events, ev)
		e.recompute()
		return nil
	}

	// Compute lag without an int64 subtraction that could overflow on extreme
	// timestamps. An event at/after the watermark has non-positive lag by
	// definition; a strictly older event is always behind by a positive amount
	// smaller than the timestamp magnitudes involved in real media clocks.
	switch {
	case ev.TimeNS >= e.watermark:
		raised := ev.TimeNS > e.watermark
		if raised {
			e.watermark = ev.TimeNS
		}
		e.events = append(e.events, ev)
		e.recompute()
		if raised {
			e.freeze()
		}
		return nil
	default:
		lag := e.watermark - ev.TimeNS
		if lag <= e.cfg.LateWindowNS {
			// Late but inside the fixed window: deterministic backtrack.
			e.events = append(e.events, ev)
			e.recomputed++
			e.recompute()
			return nil
		}
		e.rejected++
		return model.New(model.ErrLateRejected,
			"event %s at %d is %d ns behind watermark %d (window %d)",
			ev.ID, ev.TimeNS, lag, e.watermark, e.cfg.LateWindowNS).
			AtTrack(ev.Track).AtOffset(ev.TimeNS)
	}
}

// recompute rebuilds the active-window intervals from the retained events.
// Rebuilding from a sorted copy (time, then ID) is what makes the result
// independent of arrival order. Only the active window is rebuilt; frozen
// intervals are immutable and live in the hash prefix.
func (e *Engine) recompute() {
	evs := append([]Event(nil), e.events...)
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].TimeNS != evs[j].TimeNS {
			return evs[i].TimeNS < evs[j].TimeNS
		}
		return evs[i].ID < evs[j].ID
	})

	var out []Interval
	var group []Event
	flush := func(end int64) {
		if len(group) == 0 {
			return
		}
		h := newHash()
		for _, ev := range group {
			h.write(ev.ID)
			h.writeBytes(ev.Payload)
		}
		out = append(out, Interval{
			StartNS: group[0].TimeNS, EndNS: end, Count: len(group), PayloadHash: h.sum64(),
		})
	}
	for _, ev := range evs {
		if len(group) > 0 && ev.TimeNS != group[0].TimeNS {
			flush(ev.TimeNS)
			group = group[:0]
		}
		group = append(group, ev)
	}
	flush(e.watermark)
	e.active = out
}

// freeze folds intervals that can no longer change into the hash prefix and
// drops their events.
//
// The mutability boundary is an interval's StartNS, not its EndNS: an event at
// time t is grouped into the interval whose StartNS equals t (the EndNS is just
// the next group's time, or the watermark for the final group). An interval is
// immutable once its StartNS is at or behind watermark-window, because an event
// that could still join it would arrive more than one window late and be
// rejected. The predicate is a pure function of event time, so arrival order
// cannot change what gets frozen, preserving the order-independent hash.
func (e *Engine) freeze() {
	cutoff := e.watermark - e.cfg.LateWindowNS
	var stillActive []Interval
	h := resumeHash(e.prefixHash)
	for _, iv := range e.active {
		if iv.StartNS <= cutoff {
			h.writeInt64(iv.StartNS)
			h.writeInt64(iv.EndNS)
			h.writeInt(iv.Count)
			h.writeUint64(iv.PayloadHash)
			e.frozenCount++
		} else {
			stillActive = append(stillActive, iv)
		}
	}
	e.active = stillActive
	e.prefixHash = h.sum64()

	// Drop events at or before the cutoff; only the active window is retained.
	retained := e.events[:0]
	for _, ev := range e.events {
		if ev.TimeNS > cutoff {
			retained = append(retained, ev)
		}
	}
	e.events = retained
}

// Snapshot is the engine's observable, order-independent state. The hash
// resumes from the frozen prefix and appends active intervals in finalized
// order, so it covers every interval the engine has ever produced.
type Snapshot struct {
	Watermark    int64      `json:"watermark_ns"`
	Intervals    []Interval `json:"intervals"`
	Recomputed   int        `json:"late_recomputed"`
	Rejected     int        `json:"late_rejected"`
	TimelineHash uint64     `json:"timeline_hash"`
}

// Snapshot returns the current finalized state.
func (e *Engine) Snapshot() Snapshot {
	h := resumeHash(e.prefixHash)
	for _, iv := range e.active {
		h.writeInt64(iv.StartNS)
		h.writeInt64(iv.EndNS)
		h.writeInt(iv.Count)
		h.writeUint64(iv.PayloadHash)
	}
	return Snapshot{
		Watermark: e.watermark, Intervals: append([]Interval(nil), e.active...),
		Recomputed: e.recomputed, Rejected: e.rejected, TimelineHash: h.sum64(),
	}
}

// AcceptedCount is the retained (active-window) event count, bounded by the
// event density inside one late window rather than total stream length.
func (e *Engine) AcceptedCount() int { return len(e.events) }

// FrozenCount reports how many intervals have left the active window.
func (e *Engine) FrozenCount() int { return e.frozenCount }
