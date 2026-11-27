// Package mediacore is a local, dependency-free media processing kernel that
// maps and merges versioned HLS/DASH fragment playlists onto a single
// nanosecond timeline.
package mediacore

import (
	"math"
	"sort"
)

// Nano is the unified time unit of the kernel: every timeline position is an
// integer number of nanoseconds since the asset epoch.
type Nano int64

const (
	// NanosPerSecond is the number of nanoseconds in one second.
	NanosPerSecond Nano = 1_000_000_000
	// maxNano mirrors the range of an int64 nanosecond timestamp.
	maxNano Nano = math.MaxInt64
)

// ScaleChecked converts a duration expressed in timescale ticks into whole
// nanoseconds. It rejects a zero timescale, negative input and any conversion
// whose intermediate product or final result would overflow int64.
func ScaleChecked(ticks int64, timescale uint32) (Nano, error) {
	if timescale == 0 {
		return 0, Wrap(ErrZeroTimescale, "", 0, StageTimeline, -1)
	}
	if ticks < 0 {
		return 0, NewError("", 0, StageTimeline, -1, "negative media time")
	}
	t := Nano(ticks)
	scale := Nano(timescale)
	// ns = ticks * 1e9 / timescale; guard the multiplication first.
	if t > maxNano/NanosPerSecond {
		return 0, WrapMsg(ErrOverflow, "", 0, StageTimeline, -1,
			"timescale conversion overflows nanosecond range")
	}
	ns := (t * NanosPerSecond) / scale
	if ns < 0 || ns > maxNano {
		return 0, WrapMsg(ErrOverflow, "", 0, StageTimeline, -1,
			"timescale conversion overflows nanosecond range")
	}
	return ns, nil
}

// Scale converts ticks to nanoseconds and panics on an invalid timescale. It
// is only for callers that have already validated the timescale.
func Scale(ticks uint64, timescale uint32) Nano {
	ns, err := ScaleChecked(int64(ticks), timescale)
	if err != nil {
		panic(err.Error())
	}
	return ns
}

// TimedEvent is anything placed on the unified timeline. Events sharing the
// same nano timestamp are ordered deterministically by TrackID then InSeq.
type TimedEvent struct {
	At      Nano
	TrackID uint32
	InSeq   uint32
	Kind    string
	Ref     string
}

// Timeline accumulates events while enforcing a strictly non-regressing
// insertion cursor; the cursor guards against a backwards media timeline.
type Timeline struct {
	events []TimedEvent
	cursor Nano
}

// NewTimeline starts an empty timeline at the asset epoch.
func NewTimeline() *Timeline { return &Timeline{} }

// Append places an event at at. A position earlier than the current cursor is
// a timeline regression and is rejected rather than silently reordered.
func (t *Timeline) Append(ev TimedEvent, asset string) error {
	if ev.At < t.cursor {
		return WrapMsg(ErrRegression, asset, ev.TrackID, StageTimeline, -1,
			"event at "+itoa(int64(ev.At))+
				" precedes cursor "+itoa(int64(t.cursor)))
	}
	t.cursor = ev.At
	t.events = append(t.events, ev)
	return nil
}

// Advance moves the cursor by delta; a negative delta is a regression.
func (t *Timeline) Advance(delta Nano, asset string, track uint32) error {
	if delta < 0 {
		return NewError(asset, track, StageTimeline, -1, "negative timeline delta")
	}
	t.cursor += delta
	return nil
}

// Cursor returns the current high-water mark of the timeline.
func (t *Timeline) Cursor() Nano { return t.cursor }

// Events returns a copy of the events stably ordered by (At, TrackID, InSeq).
func (t *Timeline) Events() []TimedEvent {
	out := make([]TimedEvent, len(t.events))
	copy(out, t.events)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.At != b.At {
			return a.At < b.At
		}
		if a.TrackID != b.TrackID {
			return a.TrackID < b.TrackID
		}
		return a.InSeq < b.InSeq
	})
	return out
}

// Len reports how many events have been appended.
func (t *Timeline) Len() int { return len(t.events) }

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
