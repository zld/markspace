package timeline

// DefaultTickNs is the Matroska default TimestampScale: one tick = 1ms.
const DefaultTickNs uint64 = 1_000_000

// Clock is the unified nanosecond time base. All modules convert raw
// container timecode ticks to nanoseconds through a single Clock so
// repair, streaming, remux and metrics share one reference.
type Clock struct {
	// TickNs is the number of nanoseconds per container timecode tick.
	TickNs uint64
}

// NewClock returns a Clock for the given tick length in nanoseconds.
// A zero tick length falls back to the Matroska default (1ms).
func NewClock(tickNs uint64) Clock {
	if tickNs == 0 {
		tickNs = DefaultTickNs
	}
	return Clock{TickNs: tickNs}
}

// Ns converts container ticks to nanoseconds on the unified clock.
func (c Clock) Ns(ticks int64) int64 {
	return ticks * int64(c.TickNs)
}

// Ticks converts nanoseconds back to container ticks (truncating).
func (c Clock) Ticks(ns int64) int64 {
	return ns / int64(c.TickNs)
}

// NsToMs renders nanoseconds as fractional milliseconds for reports.
func NsToMs(ns int64) float64 {
	return float64(ns) / 1e6
}
