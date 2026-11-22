package timeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// Event is one timeline entry produced by manifest parsers, the RTP
// receiver or the metadata normalizer. Seq is the source sequence number;
// ID is a unique, stable source identifier.
type Event struct {
	Epoch int
	Time  Rat
	Seq   uint64
	ID    string
	Kind  string
	Data  []byte
}

// Watermark tracks the highest event time already committed. Events at or
// before the watermark are considered final unless an allowed replay
// supersedes them.
type Watermark struct {
	Epoch int
	Time  Rat
	Init  bool
}

// Advance moves the watermark forward. Backward or cross-epoch advances
// are rejected so the time line stays monotonic.
func (w *Watermark) Advance(epoch int, t Rat) error {
	if !w.Init {
		w.Epoch, w.Time, w.Init = epoch, t, true
		return nil
	}
	if epoch < w.Epoch {
		return NewError("WATERMARK_REGRESSION", "epoch moved backwards",
			map[string]any{"from": w.Epoch, "to": epoch})
	}
	if epoch == w.Epoch && t.Cmp(w.Time) < 0 {
		return NewError("WATERMARK_REGRESSION", "watermark time moved backwards",
			map[string]any{"from": w.Time.String(), "to": t.String()})
	}
	if epoch > w.Epoch {
		w.Epoch = epoch
	}
	if t.Cmp(w.Time) > 0 {
		w.Time = t
	}
	return nil
}

// ReplayMetrics counts engine activity for the end-to-end report.
type ReplayMetrics struct {
	Accepted   int
	Replayed   int
	Rejected   int
	Snapshots  int
	ReplayDepth int
}

// Snapshot is a restorable, copy-on-write view of engine state.
type Snapshot struct {
	Watermark Watermark
	Committed []Event
	Pending   []Event
	Metrics   ReplayMetrics
}

// Engine accepts events in arbitrary arrival order, commits everything at
// or before the watermark immediately and recomputes committed output when
// bounded late data arrives. ReplayWindow is the maximum distance a late
// event may lie behind the watermark; zero disables late data.
type Engine struct {
	watermark    Watermark
	pending      []Event
	committed    []Event
	replayWindow Rat
	metrics      ReplayMetrics
}

// NewEngine constructs an engine. A negative window is invalid.
func NewEngine(replayWindow Rat) (*Engine, error) {
	if replayWindow.Num < 0 {
		return nil, NewError("INVALID_CONFIG", "replay window must be non-negative",
			map[string]any{"window": replayWindow.String()})
	}
	return &Engine{replayWindow: replayWindow}, nil
}

// Watermark returns the current watermark value.
func (e *Engine) Watermark() Watermark { return e.watermark }

// Metrics returns a copy of the engine counters.
func (e *Engine) Metrics() ReplayMetrics { return e.metrics }

// Committed returns the deterministically ordered, immutable events.
func (e *Engine) Committed() []Event {
	out := make([]Event, len(e.committed))
	copy(out, e.committed)
	return out
}

// Pending returns buffered events that have not reached the watermark.
func (e *Engine) Pending() []Event {
	out := make([]Event, len(e.pending))
	copy(out, e.pending)
	return out
}

func sortEvents(evs []Event) {
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].Epoch != evs[j].Epoch {
			return evs[i].Epoch < evs[j].Epoch
		}
		if c := evs[i].Time.Cmp(evs[j].Time); c != 0 {
			return c < 0
		}
		if evs[i].Seq != evs[j].Seq {
			return evs[i].Seq < evs[j].Seq
		}
		return evs[i].ID < evs[j].ID
	})
}

// Push ingests one event. Late events inside the replay window trigger a
// bounded recomputation; events beyond it fail with REPLAY_WINDOW_EXCEEDED.
func (e *Engine) Push(ev Event) error {
	if !e.watermark.Init || ev.Epoch > e.watermark.Epoch ||
		(ev.Epoch == e.watermark.Epoch && ev.Time.Cmp(e.watermark.Time) > 0) {
		e.pending = append(e.pending, ev)
		if err := e.watermark.Advance(ev.Epoch, ev.Time); err != nil {
			return err
		}
		e.metrics.Accepted++
		e.flush()
		return nil
	}

	// Event at or behind the watermark: late data.
	late := ev
	if late.Epoch == e.watermark.Epoch {
		d, err := e.watermark.Time.Sub(late.Time)
		if err != nil {
			return err
		}
		if d.Cmp(e.replayWindow) > 0 {
			e.metrics.Rejected++
			return NewError("REPLAY_WINDOW_EXCEEDED",
				"late event falls outside the bounded replay window",
				map[string]any{
					"late_by":  d.String(),
					"window":   e.replayWindow.String(),
					"event_id": late.ID,
				})
		}
	}
	for _, c := range e.committed {
		if c.ID == ev.ID {
			e.metrics.Rejected++
			return NewError("DUPLICATE_EVENT", "event id already committed",
				map[string]any{"event_id": ev.ID})
		}
	}
	e.committed = append(e.committed, ev)
	sortEvents(e.committed)
	e.metrics.Replayed++
	e.metrics.ReplayDepth++
	return nil
}

func (e *Engine) flush() {
	kept := e.pending[:0]
	for _, ev := range e.pending {
		if ev.Epoch == e.watermark.Epoch && ev.Time.Cmp(e.watermark.Time) <= 0 {
			e.committed = append(e.committed, ev)
		} else if ev.Epoch < e.watermark.Epoch {
			e.committed = append(e.committed, ev)
		} else {
			kept = append(kept, ev)
		}
	}
	e.pending = kept
	sortEvents(e.committed)
}

// Digest hashes the canonical committed ordering. Identical input sets
// produce identical digests regardless of arrival order.
func (e *Engine) Digest() string {
	h := sha256.New()
	for _, ev := range e.committed {
		fmt.Fprintf(h, "%d|%s|%d|%s|%x\n",
			ev.Epoch, ev.Time.String(), ev.Seq, ev.Kind, ev.Data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Snapshot captures a deep copy for failure recovery.
func (e *Engine) Snapshot() Snapshot {
	s := Snapshot{
		Watermark: e.watermark,
		Committed: e.Committed(),
		Pending:   e.Pending(),
		Metrics:   e.metrics,
	}
	e.metrics.Snapshots++
	return s
}

// Restore resets the engine to a previously captured snapshot.
func (e *Engine) Restore(s Snapshot) {
	e.watermark = s.Watermark
	e.committed = append([]Event(nil), s.Committed...)
	e.pending = append([]Event(nil), s.Pending...)
	e.metrics = s.Metrics
}
