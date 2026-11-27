// Package clock defines the single nanosecond time base shared by the EBML
// parser, the Matroska reader and the client event mapper. Every conversion is
// checked for int64 overflow and reports serr.CodeClockOverflow rather than
// wrapping, so a bad timestamp can never silently alias a valid one.
package clock

import (
	"math"

	"tracecheck/internal/serr"
)

// NanosPerMilli and NanosPerSecond name the two most common scales.
const (
	NanosPerMilli  int64 = 1_000_000
	NanosPerSecond int64 = 1_000_000_000
)

// Timebase converts Matroska ticks and client clock units into nanoseconds.
type Timebase struct {
	// TickNanos is the Matroska TimestampScale: nanoseconds per tick.
	TickNanos int64
	// ClientUnitNanos is the client event clock unit in nanoseconds, for
	// example 1 for milliseconds-as-nanos or NanosPerMilli for a millisecond
	// count. It must be positive.
	ClientUnitNanos int64
}

// DefaultTimebase returns the Matroska default (1 ms ticks) treating client
// event timestamps as milliseconds.
func DefaultTimebase() Timebase {
	return Timebase{TickNanos: NanosPerMilli, ClientUnitNanos: NanosPerMilli}
}

// Validate checks the scale fields before any conversion.
func (tb Timebase) Validate() error {
	if tb.TickNanos <= 0 {
		return serr.New(serr.CodeInvalidArgs, "tick scale must be positive, got %d", tb.TickNanos)
	}
	if tb.ClientUnitNanos <= 0 {
		return serr.New(serr.CodeInvalidArgs, "client unit must be positive, got %d", tb.ClientUnitNanos)
	}
	return nil
}

// TicksToNanos converts a Matroska tick count (cluster timestamp plus block
// relative timecode) into nanoseconds, saturating instead of overflowing.
func (tb Timebase) TicksToNanos(ticks int64) (int64, error) {
	if err := tb.Validate(); err != nil {
		return 0, err
	}
	ns, ok := mulChecked(ticks, tb.TickNanos)
	if !ok {
		return 0, serr.New(serr.CodeClockOverflow,
			"%d ticks at %d ns/tick does not fit in int64 nanoseconds",
			ticks, tb.TickNanos)
	}
	return ns, nil
}

// MicrosToNanos converts microseconds to nanoseconds.
func MicrosToNanos(us int64) (int64, error) {
	ns, ok := mulChecked(us, 1_000)
	if !ok {
		return 0, serr.New(serr.CodeClockOverflow, "%d µs does not fit in int64 nanoseconds", us)
	}
	return ns, nil
}

// MillisToNanos converts milliseconds to nanoseconds.
func MillisToNanos(ms int64) (int64, error) {
	ns, ok := mulChecked(ms, NanosPerMilli)
	if !ok {
		return 0, serr.New(serr.CodeClockOverflow, "%d ms does not fit in int64 nanoseconds", ms)
	}
	return ns, nil
}

// ClientToNanos converts a raw client clock count into nanoseconds using the
// configured unit.
func (tb Timebase) ClientToNanos(raw int64) (int64, error) {
	if err := tb.Validate(); err != nil {
		return 0, err
	}
	ns, ok := mulChecked(raw, tb.ClientUnitNanos)
	if !ok {
		return 0, serr.New(serr.CodeClockOverflow,
			"client time %d at %d ns/unit does not fit in int64 nanoseconds",
			raw, tb.ClientUnitNanos)
	}
	return ns, nil
}

// NanosToTicks converts nanoseconds back to Matroska ticks, truncating toward
// zero. It is used to place an event on the block timeline.
func (tb Timebase) NanosToTicks(ns int64) (int64, error) {
	if err := tb.Validate(); err != nil {
		return 0, err
	}
	return ns / tb.TickNanos, nil
}

// mulChecked multiplies two int64 values and reports false on overflow.
func mulChecked(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a > 0 {
		if b > 0 {
			if a > math.MaxInt64/b {
				return 0, false
			}
		} else if b < math.MinInt64/a {
			return 0, false
		}
	} else {
		if b > 0 {
			if a < math.MinInt64/b {
				return 0, false
			}
		} else if a != 0 && b < math.MaxInt64/a {
			return 0, false
		}
	}
	return a * b, true
}
