// Package media provides the shared rational time base and the structured
// fault model used by every stage of the recovery pipeline.
package media

import (
	"errors"
	"fmt"
)

// Clock is a rational time base expressed in ticks per second.
type Clock struct {
	timescale uint32
}

// NewClock validates and constructs a time base.
func NewClock(timescale uint32) (Clock, error) {
	if timescale == 0 {
		return Clock{}, errors.New("media: timescale must be positive")
	}
	return Clock{timescale: timescale}, nil
}

// MustClock is NewClock for package-level constants and tests.
func MustClock(timescale uint32) Clock {
	c, err := NewClock(timescale)
	if err != nil {
		panic(err)
	}
	return c
}

// Timescale returns ticks per second.
func (c Clock) Timescale() uint32 { return c.timescale }

// Valid reports whether the clock was initialized.
func (c Clock) Valid() bool { return c.timescale != 0 }

// Millis converts a tick count to milliseconds, rounding half away from zero.
func (c Clock) Millis(ticks int64) int64 {
	if c.timescale == 0 {
		return 0
	}
	return divRound(ticks*1000, int64(c.timescale))
}

// Ticks converts milliseconds to a tick count, rounding half away from zero.
func (c Clock) Ticks(ms int64) int64 {
	if c.timescale == 0 {
		return 0
	}
	return divRound(ms*int64(c.timescale), 1000)
}

// Convert re-bases ticks from one clock onto another without passing through
// milliseconds, so no rounding is introduced beyond the final division.
func (c Clock) Convert(ticks int64, from Clock) int64 {
	if !c.Valid() || !from.Valid() {
		return 0
	}
	return divRound(ticks*int64(c.timescale), int64(from.timescale))
}

// At expresses an instant as an exact rational number of seconds.
func (c Clock) At(ticks int64) Rational {
	return Rational{Num: ticks, Den: int64(c.timescale)}
}

// Rational is an exact fraction of a second. Den is always positive and the
// fraction is reduced.
type Rational struct {
	Num int64
	Den int64
}

// NewRational constructs a reduced fraction.
func NewRational(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, errors.New("media: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(abs(num), den)
	return Rational{Num: num / g, Den: den / g}, nil
}

// MustRational is NewRational for deterministic literals.
func MustRational(num, den int64) Rational {
	r, err := NewRational(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// Add returns r+o for fractions sharing a benign magnitude.
func (r Rational) Add(o Rational) Rational {
	n, d := r.Num*o.Den+o.Num*r.Den, r.Den*o.Den
	out, err := NewRational(n, d)
	if err != nil {
		return Rational{}
	}
	return out
}

// Sub returns r-o.
func (r Rational) Sub(o Rational) Rational {
	return r.Add(Rational{Num: -o.Num, Den: o.Den})
}

// Compare returns -1, 0 or 1.
func (r Rational) Compare(o Rational) int {
	l, rr := r.Num*o.Den, o.Num*r.Den
	switch {
	case l < rr:
		return -1
	case l > rr:
		return 1
	default:
		return 0
	}
}

// Millis returns the exact fraction in milliseconds, rounded.
func (r Rational) Millis() int64 { return divRound(r.Num*1000, r.Den) }

// Ticks returns the fraction expressed on the given clock.
func (r Rational) Ticks(c Clock) int64 {
	if !c.Valid() {
		return 0
	}
	return divRound(r.Num*int64(c.timescale), r.Den)
}

// TickInterval is a half-open time range [Start,End) on a clock.
type TickInterval struct {
	Start int64
	End   int64
	Clock Clock
}

// NewInterval constructs an interval; end is clamped to at least start.
func NewInterval(start, end int64, c Clock) TickInterval {
	if end < start {
		end = start
	}
	return TickInterval{Start: start, End: end, Clock: c}
}

// Empty reports a zero-width interval.
func (iv TickInterval) Empty() bool { return iv.End <= iv.Start }

// StartMillis returns the normalized closed end of the interval in ms.
func (iv TickInterval) StartMillis() int64 { return iv.Clock.Millis(iv.Start) }

// EndMillis returns the normalized open end of the interval in ms.
func (iv TickInterval) EndMillis() int64 { return iv.Clock.Millis(iv.End) }

// String renders the interval with both native ticks and normalized ms.
func (iv TickInterval) String() string {
	return fmt.Sprintf("ticks=[%d,%d) ms=[%d,%d)", iv.Start, iv.End, iv.StartMillis(), iv.EndMillis())
}

func divRound(num, den int64) int64 {
	if den == 0 {
		return 0
	}
	if den < 0 {
		num, den = -num, -den
	}
	if num >= 0 {
		return (num + den/2) / den
	}
	return -((-num + den/2) / den)
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
