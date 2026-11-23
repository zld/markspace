// Package timeline turns the ordered RTP event stream into a watermark time
// line. The watermark advances strictly on event time, accepts late events
// inside the configured window for deterministic replay, and refuses any
// data that falls into a sealed (already archived) interval.
package timeline

import (
	"sort"

	"mediaverify/internal/clock"
	"mediaverify/internal/errs"
)

// Event is one time-line observation. Seq is the stable, monotonic event
// number assigned at ingest; it never changes even when a late event is
// inserted retrospectively.
type Event struct {
	Seq      uint64
	Time     clock.Ticks
	Keyframe bool
	Bytes    uint64
}

// Interval marks a time span that cannot be trusted (e.g. burst loss across
// a keyframe). Content inside is never fabricated.
type Interval struct {
	Start  clock.Ticks
	End    clock.Ticks
	Reason string
}

// Decision classifies what the watermark did with an offered event.
type Decision int

const (
	DecisionAdvance  Decision = iota // event at or after the watermark
	DecisionReplay                   // late event accepted, suffix recomputed
	DecisionRejected                 // outside the late window or sealed
)

// RejectedEvent records the reason a late event was refused.
type RejectedEvent struct {
	Event  Event
	Reason string
}

// Config configures the watermark.
type Config struct {
	LateWindow clock.Ticks // maximum age of an acceptable late event
}

// Watermark is the shared time-line state machine.
type Watermark struct {
	cfg         Config
	level       clock.Ticks
	sealedUntil clock.Ticks
	nextSeq     uint64
	events      []Event
	intervals   []Interval
	replayCount int
	rejected    []RejectedEvent
}

// New builds an empty watermark.
func New(cfg Config) *Watermark {
	return &Watermark{cfg: cfg}
}

// Level is the current high-water mark (newest committed event time).
func (w *Watermark) Level() clock.Ticks { return w.level }

// SealedUntil is the end of the archived prefix.
func (w *Watermark) SealedUntil() clock.Ticks { return w.sealedUntil }

// ReplayCount reports how many accepted late events triggered recomputation.
func (w *Watermark) ReplayCount() int { return w.replayCount }

// NextSeq reserves the next stable event sequence number.
func (w *Watermark) NextSeq() uint64 {
	w.nextSeq++
	return w.nextSeq
}

// Push offers an event whose Seq was obtained from NextSeq. Events at or
// after the level advance the watermark; older events inside the window are
// inserted and flagged for replay; older or sealed events are rejected.
func (w *Watermark) Push(ev Event) (Decision, error) {
	if ev.Seq == 0 {
		return DecisionRejected, errs.New(errs.KindInvalidInput, "timeline",
			"event seq must be allocated with NextSeq")
	}
	switch {
	case ev.Time < w.sealedUntil:
		w.rejected = append(w.rejected, RejectedEvent{ev, "interval sealed: archived prefix is immutable"})
		return DecisionRejected, errs.New(errs.KindSealed, "timeline",
			"event at %d rejected: before sealed boundary %d", ev.Time.Ns(), w.sealedUntil.Ns())
	case len(w.events) > 0 && ev.Time < w.level:
		if w.level-ev.Time > w.cfg.LateWindow {
			reason := "late event beyond allowed late window"
			w.rejected = append(w.rejected, RejectedEvent{ev, reason})
			return DecisionRejected, errs.New(errs.KindSealed, "timeline",
				"event at %d rejected: age %d exceeds late window %d",
				ev.Time.Ns(), (w.level - ev.Time).Ns(), w.cfg.LateWindow.Ns())
		}
		w.insertSorted(ev)
		w.replayCount++
		return DecisionReplay, nil
	default:
		w.events = append(w.events, ev)
		w.level = ev.Time
		return DecisionAdvance, nil
	}
}

// insertSorted places a late event by (time, seq) so recomputation is
// deterministic regardless of arrival order.
func (w *Watermark) insertSorted(ev Event) {
	i := sort.Search(len(w.events), func(i int) bool {
		if w.events[i].Time != ev.Time {
			return w.events[i].Time > ev.Time
		}
		return w.events[i].Seq > ev.Seq
	})
	w.events = append(w.events, Event{})
	copy(w.events[i+1:], w.events[i:])
	w.events[i] = ev
}

// Seal archives the prefix up to and including t. Later events before t are
// refused. Sealing only moves forward.
func (w *Watermark) Seal(t clock.Ticks) error {
	if t < w.sealedUntil {
		return errs.New(errs.KindInternal, "timeline",
			"seal cannot move backwards: %d < %d", t.Ns(), w.sealedUntil.Ns())
	}
	w.sealedUntil = t
	return nil
}

// Events returns a copy of all committed events in canonical order.
func (w *Watermark) Events() []Event {
	out := make([]Event, len(w.events))
	copy(out, w.events)
	return out
}

// AddInterval records an untrusted span; intervals are merged on read.
func (w *Watermark) AddInterval(in Interval) error {
	if in.End <= in.Start {
		return errs.New(errs.KindInvalidInput, "timeline",
			"interval must have positive duration: %d..%d", in.Start.Ns(), in.End.Ns())
	}
	w.intervals = append(w.intervals, in)
	return nil
}

// Untrusted returns merged, time-ordered untrusted intervals.
func (w *Watermark) Untrusted() []Interval {
	if len(w.intervals) == 0 {
		return nil
	}
	ins := make([]Interval, len(w.intervals))
	copy(ins, w.intervals)
	sort.Slice(ins, func(i, j int) bool {
		if ins[i].Start != ins[j].Start {
			return ins[i].Start < ins[j].Start
		}
		return ins[i].End < ins[j].End
	})
	merged := []Interval{ins[0]}
	for _, in := range ins[1:] {
		last := &merged[len(merged)-1]
		if in.Start <= last.End {
			if in.End > last.End {
				last.End = in.End
			}
			if last.Reason != "" && in.Reason != "" && last.Reason != in.Reason {
				last.Reason = last.Reason + "+" + in.Reason
			}
		} else {
			merged = append(merged, in)
		}
	}
	return merged
}

// Rejected returns the recorded late-event rejections.
func (w *Watermark) Rejected() []RejectedEvent {
	out := make([]RejectedEvent, len(w.rejected))
	copy(out, w.rejected)
	return out
}
