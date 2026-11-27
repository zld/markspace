// Package timeline provides the shared integer sample clock and the common
// error/track model used by every subsystem. All timeline arithmetic is
// integer-only: no floating point accumulation is permitted anywhere.
package timeline

import (
	"errors"
	"fmt"
	"math/bits"
	"time"
)

// Ticks is an integer count of media clock ticks.
type Ticks int64

// Clock is an integer sample clock with a fixed timescale (ticks per second).
type Clock struct {
	Timescale int64 `json:"timescale"`
}

// Validate rejects non-positive timescales.
func (c Clock) Validate() error {
	if c.Timescale <= 0 {
		return fmt.Errorf("timeline: invalid timescale %d", c.Timescale)
	}
	return nil
}

// ErrOverflow is returned when a conversion cannot be represented in int64.
var ErrOverflow = errors.New("timeline: tick conversion overflow")

// mulDivRound computes round(a*b/d) using 128-bit intermediate precision,
// rounding half away from zero. d must be positive.
func mulDivRound(a, b, d int64) (int64, error) {
	if d <= 0 {
		return 0, fmt.Errorf("timeline: non-positive divisor %d", d)
	}
	neg := false
	if a < 0 {
		neg = !neg
		a = -a
	}
	if b < 0 {
		neg = !neg
		b = -b
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	ud := uint64(d)
	if hi >= ud {
		return 0, ErrOverflow
	}
	q, rem := bits.Div64(hi, lo, ud)
	// Round half away from zero: 2*rem >= d, computed without overflow.
	if rem >= ud-rem {
		q++
	}
	if q > uint64(1<<63-1) {
		return 0, ErrOverflow
	}
	v := int64(q)
	if neg {
		v = -v
	}
	return v, nil
}

// Convert re-expresses t (measured on this clock) on the dst clock, rounding
// to the nearest tick. The round-trip error is at most one tick of dst.
func (c Clock) Convert(t Ticks, dst Clock) (Ticks, error) {
	if err := c.Validate(); err != nil {
		return 0, err
	}
	if err := dst.Validate(); err != nil {
		return 0, err
	}
	v, err := mulDivRound(int64(t), dst.Timescale, c.Timescale)
	if err != nil {
		return 0, err
	}
	return Ticks(v), nil
}

// FromRational returns the tick count for num/den seconds on this clock.
func (c Clock) FromRational(num, den int64) (Ticks, error) {
	if err := c.Validate(); err != nil {
		return 0, err
	}
	v, err := mulDivRound(num, c.Timescale, den)
	if err != nil {
		return 0, err
	}
	return Ticks(v), nil
}

// ToDuration converts ticks to a time.Duration (nanosecond resolution).
func (c Clock) ToDuration(t Ticks) (time.Duration, error) {
	ns, err := c.Convert(t, Clock{Timescale: int64(time.Second)})
	if err != nil {
		return 0, err
	}
	return time.Duration(ns), nil
}
