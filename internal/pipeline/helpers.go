package pipeline

import "medfix/internal/clock"

// clockFromTicks wraps clock.FromTicks so stage code stays focused on
// pipeline semantics rather than conversion mechanics.
func clockFromTicks(ticks int64, timescale uint32) int64 {
	return clock.FromTicks(ticks, timescale)
}
