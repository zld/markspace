package timeline

import (
	"sort"

	"github.com/zld/mediarc/internal/model"
)

// Disposition describes how the watermark treats one arrival.
type Disposition string

const (
	OnTime   Disposition = "on_time"
	Buffered Disposition = "buffered"
	Late     Disposition = "late_beyond_window"
)

// WatermarkState is the serializable watermark checkpoint.
type WatermarkState struct {
	Started           bool     `json:"started"`
	MaxEventNS        model.NS `json:"max_event_ns"`
	WatermarkNS       model.NS `json:"watermark_ns"`
	AllowedLatenessNS model.NS `json:"allowed_lateness_ns"`
}

// Watermark tracks max event time minus the configured allowed lateness.
type Watermark struct {
	state WatermarkState
}

// NewWatermark creates a tracker for one allowed-lateness window.
func NewWatermark(allowed model.NS) *Watermark {
	return &Watermark{state: WatermarkState{AllowedLatenessNS: allowed}}
}

// State returns a checkpoint copy.
func (w *Watermark) State() WatermarkState { return w.state }

// Restore sets checkpoint state.
func (w *Watermark) Restore(st WatermarkState) { w.state = st }

// Observe updates the maximum and classifies t. A late observation does not
// move the watermark backwards.
func (w *Watermark) Observe(t model.NS) Disposition {
	if !w.state.Started || t > w.state.MaxEventNS {
		w.state.Started = true
		w.state.MaxEventNS = t
	}
	w.state.WatermarkNS = w.state.MaxEventNS - w.state.AllowedLatenessNS
	if t < w.state.WatermarkNS {
		return Late
	}
	if t < w.state.MaxEventNS {
		return Buffered
	}
	return OnTime
}

// EventBuffer is a bounded, deterministically ordered out-of-order cache.
type EventBuffer struct {
	events []Event
	limit  int
}

// NewEventBuffer creates a buffer capped at limit events.
func NewEventBuffer(limit int) *EventBuffer {
	if limit <= 0 {
		limit = 1
	}
	return &EventBuffer{limit: limit}
}

// Insert stores an event in shared-time order. Duplicate times retain arrival
// order because insertion uses stable sorting.
func (b *EventBuffer) Insert(ev Event) error {
	if len(b.events) >= b.limit {
		return &model.ConfigError{
			Field: "limits.max_buffered_events",
			Value: b.limit,
			Err:   model.ErrCheckpoint,
		}
	}
	b.events = append(b.events, ev)
	sort.SliceStable(b.events, func(i, j int) bool {
		if b.events[i].TimeNS != b.events[j].TimeNS {
			return b.events[i].TimeNS < b.events[j].TimeNS
		}
		return b.events[i].Raw.ArrivalIndex < b.events[j].Raw.ArrivalIndex
	})
	return nil
}

// Events returns the buffered events in deterministic order.
func (b *EventBuffer) Events() []Event { return append([]Event(nil), b.events...) }

// Len returns the buffered event count.
func (b *EventBuffer) Len() int { return len(b.events) }

// PruneBelow removes events strictly older than watermark and returns them.
func (b *EventBuffer) PruneBelow(watermark model.NS) []Event {
	kept := b.events[:0]
	var pruned []Event
	for _, ev := range b.events {
		if ev.TimeNS < watermark {
			pruned = append(pruned, ev)
		} else {
			kept = append(kept, ev)
		}
	}
	b.events = kept
	return pruned
}

// OrderedByTime returns a stable copy sorted by shared time and arrival.
func OrderedByTime(events []Event) []Event {
	out := append([]Event(nil), events...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TimeNS != out[j].TimeNS {
			return out[i].TimeNS < out[j].TimeNS
		}
		return out[i].Raw.ArrivalIndex < out[j].Raw.ArrivalIndex
	})
	return out
}
