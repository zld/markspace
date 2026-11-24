// Package clock unifies every media timestamp onto a single nanosecond
// timebase. Matroska stores per-track tick timestamps (e.g. VP9 at a
// millisecond cluster scale, Opus at 1ms blocks); downstream modules must
// never compare raw ticks across tracks, so all conversion happens here.
package clock

import (
	"fmt"

	"mediapack/internal/fault"
)

// NanosecondsPerSecond is the resolution of the unified timebase.
const NanosecondsPerSecond int64 = 1_000_000_000

// Timebase converts ticks in a track's own clock into absolute nanoseconds.
// A zero TickRate is illegal; nanosecond conversion uses exact integer
// arithmetic (scale then divide) so no drift accumulates across a run.
type Timebase struct {
	// TickRate is ticks per second, e.g. 1000 for millisecond timestamps.
	TickRate uint64
	// OffsetNS is added after conversion, allowing per-track offsets.
	OffsetNS int64
}

// New validates a tick rate and returns the Timebase.
func New(tickRate uint64, offsetNS int64) (*Timebase, error) {
	if tickRate == 0 {
		return nil, fault.New(fault.KindTimestampRegression, -1,
			"timebase requires a non-zero tick rate")
	}
	return &Timebase{TickRate: tickRate, OffsetNS: offsetNS}, nil
}

// ToNanos converts a signed tick timestamp into nanoseconds.
func (t *Timebase) ToNanos(ticks int64) int64 {
	return ticks*NanosecondsPerSecond/int64(t.TickRate) + t.OffsetNS
}

// FromNanos converts nanoseconds back into this timebase's ticks (truncated
// toward zero), useful when writing containers that demand tick timestamps.
func (t *Timebase) FromNanos(nanos int64) int64 {
	return (nanos - t.OffsetNS) * int64(t.TickRate) / NanosecondsPerSecond
}

// Monitor enforces per-track monotonicity on the unified nanosecond timeline.
// It never guesses or repairs: a negative or regressing timestamp becomes a
// structured fault carrying the block's input offset.
type Monitor struct {
	track    int
	lastNS   int64
	hasLast  bool
	base     *Timebase
}

// NewMonitor creates a monotonicity guard for one track.
func NewMonitor(track int, base *Timebase) *Monitor {
	return &Monitor{track: track, base: base}
}

// Check converts ticks to nanoseconds and rejects the first timestamp that is
// negative or earlier than a previously accepted one. On success it records
// the timestamp and returns the nanosecond value.
func (m *Monitor) Check(ticks int64, blockOffset int64) (int64, error) {
	ns := m.base.ToNanos(ticks)
	if ns < 0 {
		return 0, fault.NewTrack(fault.KindTimestampRegression, m.track, blockOffset,
			"negative timestamp %d ticks -> %d ns", ticks, ns)
	}
	if m.hasLast && ns < m.lastNS {
		return 0, fault.NewTrack(fault.KindTimestampRegression, m.track, blockOffset,
			"timestamp regression: %d ns < previous %d ns (ticks=%d)",
			ns, m.lastNS, ticks).AsRecoverable()
	}
	m.lastNS = ns
	m.hasLast = true
	return ns, nil
}

// Last returns the most recently accepted nanosecond timestamp.
func (m *Monitor) Last() (int64, bool) { return m.lastNS, m.hasLast }

// String aids summaries and test diagnostics.
func (t *Timebase) String() string {
	return fmt.Sprintf("Timebase{rate=%d ticks/s, offset=%d ns}", t.TickRate, t.OffsetNS)
}
