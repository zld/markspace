// Package timeline defines the single nanosecond time base shared by every
// medtrace subsystem and the deterministic merge rules for multi-track media
// events. All timestamps are int64 nanosecond offsets from one shared epoch.
package timeline

import (
	"medtrace/internal/mederr"
)

// Timestamp is an absolute or relative point on the shared nanosecond clock.
type Timestamp int64

// Clock converts a track's native tick counter onto the shared nanosecond
// base. TickNanos is the clock period in nanoseconds (Matroska TimecodeScale,
// an RTP clock rate inverse, etc.).
type Clock struct {
	Name      string
	TickNanos int64
}

// NewClock validates the period and returns a named clock.
func NewClock(name string, tickNanos int64) (*Clock, error) {
	if tickNanos <= 0 {
		return nil, mederr.New(mederr.CatInvalidInput, "clock %q period must be positive, got %d", name, tickNanos)
	}
	return &Clock{Name: name, TickNanos: tickNanos}, nil
}

// Convert maps a native tick count to nanoseconds, rejecting overflow rather
// than silently wrapping.
func (c *Clock) Convert(ticks int64) (Timestamp, error) {
	v, err := mul64(ticks, c.TickNanos)
	if err != nil {
		return 0, mederr.New(mederr.CatOverflow,
			"clock %q conversion overflows: %d ticks * %d ns", c.Name, ticks, c.TickNanos).Wrap(err)
	}
	return Timestamp(v), nil
}

// Add returns t+d, rejecting overflow.
func (t Timestamp) Add(d int64) (Timestamp, error) {
	v, err := add64(int64(t), d)
	if err != nil {
		return 0, mederr.New(mederr.CatOverflow, "timestamp addition overflows: %d + %d", t, d).Wrap(err)
	}
	return Timestamp(v), nil
}

// AbsDiff returns the absolute distance between two timestamps.
func AbsDiff(a, b Timestamp) int64 {
	if a > b {
		return int64(a - b)
	}
	return int64(b - a)
}

func mul64(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	r := a * b
	if r/a != b {
		return 0, mederr.New(mederr.CatOverflow, "integer multiplication overflow: %d * %d", a, b)
	}
	return r, nil
}

func add64(a, b int64) (int64, error) {
	r := a + b
	if (r^a)&(r^b) < 0 {
		return 0, mederr.New(mederr.CatOverflow, "integer addition overflow: %d + %d", a, b)
	}
	return r, nil
}
