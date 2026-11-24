// Package timeline maintains an event-time watermark and recomputes affected
// intervals deterministically when late data arrives inside a fixed window.
//
// The engine never depends on arrival order for its final state: the same
// event set, in any order, yields the same interval summaries and the same
// timeline hash. Events beyond the window are rejected with a structured
// LATE_REJECTED error rather than silently dropped.
package timeline

import (
	"sort"

	"mediad/internal/model"
)

// Event is one timestamped piece of media state. Payload is opaque; the
// engine hashes the raw payload bytes so recomputation stays reproducible.
type Event struct {
	ID       string
	TimeNS   int64 // event time on the shared nanosecond clock
	Track    int
	Payload  []byte
}

// Interval is one finalized bucket between consecutive event times. Its
// summary is derived solely from the events inside it, never from order.
type Interval struct {
	StartNS   int64  `json:"start_ns"`
	EndNS     int64  `json:"end_ns"`
	Count     int    `json:"count"`
	PayloadHash uint64 `json:"payload_hash"`
}

// EngineConfig parameterizes the recompute policy.
type EngineConfig struct {
	// LateWindowNS is how far behind the watermark an event may still arrive.
	LateWindowNS int64
}

// Engine is the stateful watermark + recompute machine.
type Engine struct {
	cfg      EngineConfig
	watermark int64
	started  bool
	// events holds every accepted event keyed by event time for recompute.
	events []Event
	// finalized intervals produced by the latest recomputation.
	intervals []Interval
	recomputed int
	rejected   int
}

// NewEngine validates the config and returns an engine.
func NewEngine(cfg EngineConfig) (*Engine, error) {
	if cfg.LateWindowNS <= 0 {
		return nil, model.New(model.ErrLateRejected, "late window must be positive, got %d", cfg.LateWindowNS)
	}
	return &Engine{cfg: cfg}, nil
}

// Push accepts one event. An event newer than the watermark advances it; an
// event within the window behind the watermark triggers interval recompute;
// an event beyond the window is rejected with LATE_REJECTED.
func (e *Engine) Push(ev Event) error {
	if !e.started {
		e.started = true
		e.watermark = ev.TimeNS
		e.events = append(e.events, ev)
		e.recompute()
		return nil
	}

	lag := e.watermark - ev.TimeNS
	switch {
	case lag <= 0:
		// On-time or leading: accept and possibly raise the watermark.
		if ev.TimeNS > e.watermark {
			e.watermark = ev.TimeNS
		}
		e.events = append(e.events, ev)
		e.recompute()
		return nil
	case lag <= e.cfg.LateWindowNS:
		// Late but inside the fixed window: deterministic backtrack.
		e.events = append(e.events, ev)
		e.recomputed++
		e.recompute()
		return nil
	default:
		e.rejected++
		return model.New(model.ErrLateRejected,
			"event %s at %d is %d ns behind watermark %d (window %d)",
			ev.ID, ev.TimeNS, lag, e.watermark, e.cfg.LateWindowNS).
			AtTrack(ev.Track).AtOffset(ev.TimeNS)
	}
}

// recompute rebuilds every interval from scratch from the accepted events.
// Rebuilding (rather than patching) is what makes the output independent of
// arrival order. The event slice is sorted by (time, ID) before hashing.
func (e *Engine) recompute() {
	evs := make([]Event, len(e.events))
	copy(evs, e.events)
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].TimeNS != evs[j].TimeNS {
			return evs[i].TimeNS < evs[j].TimeNS
		}
		return evs[i].ID < evs[j].ID
	})

	// Group by event time; each distinct time closes one interval.
	var out []Interval
	var group []Event
	flush := func(end int64) {
		if len(group) == 0 {
			return
		}
		start := group[0].TimeNS
		h := newHash()
		for _, ev := range group {
			h.write(ev.ID)
			h.writeBytes(ev.Payload)
		}
		out = append(out, Interval{
			StartNS: start, EndNS: end, Count: len(group), PayloadHash: h.sum64(),
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
	e.intervals = out
}

// Snapshot is the engine's observable, order-independent state.
type Snapshot struct {
	Watermark   int64      `json:"watermark_ns"`
	Intervals   []Interval `json:"intervals"`
	Recomputed  int        `json:"late_recomputed"`
	Rejected    int        `json:"late_rejected"`
	// TimelineHash covers every interval in finalized order; stable across
	// arrival permutations of the same event set.
	TimelineHash uint64 `json:"timeline_hash"`
}

// Snapshot returns the current finalized state.
func (e *Engine) Snapshot() Snapshot {
	h := newHash()
	for _, iv := range e.intervals {
		h.writeInt64(iv.StartNS)
		h.writeInt64(iv.EndNS)
		h.writeInt(iv.Count)
		h.writeUint64(iv.PayloadHash)
	}
	return Snapshot{
		Watermark: e.watermark, Intervals: append([]Interval(nil), e.intervals...),
		Recomputed: e.recomputed, Rejected: e.rejected, TimelineHash: h.sum64(),
	}
}

// AcceptedCount is the number of events currently held for recompute.
func (e *Engine) AcceptedCount() int { return len(e.events) }
