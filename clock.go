package mediakernel

import (
	"fmt"
	"math"
)

// Clock converts per-track Matroska timecodes to the shared integer
// nanosecond timebase and enforces monotonicity. Missing, regressed or
// overflowing timestamps are reported, never guessed.
type Clock struct {
	SegmentScale uint64 // nanoseconds per timecode tick
	reg          *TrackRegistry
	last         map[uint64]int64
	seen         map[uint64]bool
}

func NewClock(segmentScale uint64, reg *TrackRegistry) *Clock {
	if segmentScale == 0 {
		segmentScale = 1_000_000
	}
	return &Clock{SegmentScale: segmentScale, reg: reg,
		last: make(map[uint64]int64), seen: make(map[uint64]bool)}
}

// Stamp converts ticks to nanoseconds for track, checking overflow and
// regression. offset is the byte offset of the offending block for errors.
func (c *Clock) Stamp(track uint64, ticks int64, offset int64) (int64, error) {
	t := c.reg.GetOrCreate(track)
	scale := c.SegmentScale
	abs := ticks
	if abs < 0 {
		abs = -abs
	}
	if uint64(abs) > math.MaxInt64/scale {
		return 0, &Error{ErrTSOverflow, offset, "",
			fmt.Sprintf("timecode %d ticks overflows at scale %d ns", ticks, scale)}
	}
	ns := ticks * int64(scale)
	if ts := t.TimecodeScale; ts != 0 && ts != 1.0 {
		f := float64(ns) * ts
		if math.Abs(f) > math.MaxInt64 {
			return 0, &Error{ErrTSOverflow, offset, "",
				fmt.Sprintf("timecode %d ns overflows track scale %g", ns, ts)}
		}
		ns = int64(f)
	}
	if c.seen[track] && ns < c.last[track] {
		return 0, &Error{ErrTSRegression, offset, "",
			fmt.Sprintf("track %d time regressed: %d ns < previous %d ns", track, ns, c.last[track])}
	}
	c.seen[track] = true
	c.last[track] = ns
	return ns, nil
}

// Snapshot returns the per-track last timestamps for checkpointing.
func (c *Clock) Snapshot() map[uint64]int64 {
	m := make(map[uint64]int64, len(c.last))
	for t, ns := range c.last {
		m[t] = ns
	}
	return m
}

// Restore loads a previously snapshotted state.
func (c *Clock) Restore(m map[uint64]int64) {
	for t, ns := range m {
		c.last[t] = ns
		c.seen[t] = true
	}
}
