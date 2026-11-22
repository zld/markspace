// Package timeline maintains the event-time watermark and deterministic
// replay of late-arriving samples. The watermark advances strictly forward;
// events behind it are classified late and routed to replay rather than being
// double-counted in the primary output.
package timeline

import (
	"sort"

	"mediatimeline/internal/clock"
	"mediatimeline/internal/container"
)

// Watermark is the high-water mark of delivered event time in canonical
// nanoseconds. It can never move backwards once advanced.
type Watermark struct {
	markNS       uint64
	started      bool
	lateCount    uint64
	totalCount   uint64
	lastOrderKey orderKey
}

type orderKey struct {
	dtsNS   uint64
	trackID uint32
	sample  int64
}

func keyOf(e container.SampleEvent) orderKey {
	return orderKey{e.DTSNanos, e.TrackID, e.SampleIndex}
}

// Advance presents an event. On-time or future events advance the mark and are
// accepted into the primary stream. Events strictly behind the mark are
// classified late without moving it. The first event always establishes it.
func (w *Watermark) Advance(e container.SampleEvent) (late bool) {
	w.totalCount++
	if !w.started {
		w.started = true
		w.markNS = e.DTSNanos
		w.lastOrderKey = keyOf(e)
		return false
	}
	if e.DTSNanos < w.markNS {
		w.lateCount++
		return true
	}
	if e.DTSNanos > w.markNS {
		w.markNS = e.DTSNanos
	}
	w.lastOrderKey = keyOf(e)
	return false
}

// MarkNS returns the current watermark in nanoseconds.
func (w *Watermark) MarkNS() uint64 { return w.markNS }

// LateCount reports how many events arrived behind the mark.
func (w *Watermark) LateCount() uint64 { return w.lateCount }

// TotalCount reports every event presented, including late and duplicates.
func (w *Watermark) TotalCount() uint64 { return w.totalCount }

// Started reports whether any event has been observed.
func (w *Watermark) Started() bool { return w.started }

// Snapshot captures restartable watermark state.
type WMSnapshot struct {
	MarkNS     uint64
	Started    bool
	LateCount  uint64
	TotalCount uint64
	LastKey    orderKey
}

// Snapshot returns the watermark state.
func (w *Watermark) Snapshot() WMSnapshot {
	return WMSnapshot{w.markNS, w.started, w.lateCount, w.totalCount, w.lastOrderKey}
}

// Restore rehydrates the watermark from a checkpoint.
func (w *Watermark) Restore(s WMSnapshot) {
	w.markNS, w.started, w.lateCount, w.totalCount, w.lastOrderKey =
		s.MarkNS, s.Started, s.LateCount, s.TotalCount, s.LastKey
}

// StableSort orders events by the canonical replay key (DTS, track, sample),
// preserving insertion order for equal keys.
func StableSort(events []container.SampleEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		return container.EventOrder(events[i], events[j])
	})
}

// AssertMonotonic verifies that a delivered batch is non-decreasing in DTS.
// A reversal is reported with the offending sample's position.
func AssertMonotonic(events []container.SampleEvent) error {
	for i := 1; i < len(events); i++ {
		if events[i].DTSNanos < events[i-1].DTSNanos {
			return clock.NewError(clock.ErrTimestampReversal,
				"delivered batch is not monotonic").
				WithPosition(events[i].FileOffset, events[i].TrackID, events[i].SampleIndex)
		}
	}
	return nil
}
