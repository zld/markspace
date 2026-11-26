// Package media defines the integer time base and the shared error model
// used by every pipeline subsystem. All duration arithmetic stays in
// integer ticks; no floating-point accumulation is performed anywhere.
package media

import (
	"errors"
	"fmt"
)

// TimeBase is an integral media clock: TicksPerSecond units elapse per
// wall-clock second. A zero TimeBase is invalid.
type TimeBase struct {
	TicksPerSecond uint32
}

// NewTimeBase validates and returns a TimeBase.
func NewTimeBase(ticksPerSecond uint32) (TimeBase, error) {
	if ticksPerSecond == 0 {
		return TimeBase{}, errors.New("media: time base must have a non-zero ticks-per-second")
	}
	return TimeBase{TicksPerSecond: ticksPerSecond}, nil
}

// Valid reports whether the time base is usable.
func (t TimeBase) Valid() bool { return t.TicksPerSecond != 0 }

// String renders the clock for reports and manifest entries.
func (t TimeBase) String() string {
	return fmt.Sprintf("%dHz", t.TicksPerSecond)
}

// Duration is a length of time measured in whole ticks of a TimeBase.
type Duration struct {
	Ticks int64
	Base  TimeBase
}

// NewDuration constructs a Duration and rejects negative or unbased input.
func NewDuration(ticks int64, base TimeBase) (Duration, error) {
	if !base.Valid() {
		return Duration{}, errors.New("media: duration requires a valid time base")
	}
	if ticks < 0 {
		return Duration{}, fmt.Errorf("media: negative duration ticks: %d", ticks)
	}
	return Duration{Ticks: ticks, Base: base}, nil
}

// Zero returns the zero-length duration on the given clock.
func Zero(base TimeBase) Duration { return Duration{Ticks: 0, Base: base} }

// Add returns d+other. Clocks must match; mixing clocks is a programming
// error rather than a silently converted value.
func (d Duration) Add(other Duration) (Duration, error) {
	if !d.Base.Valid() {
		return Duration{}, errors.New("media: add on invalid duration")
	}
	if d.Base != other.Base {
		return Duration{}, fmt.Errorf("media: cannot add durations on different clocks %s vs %s", d.Base, other.Base)
	}
	sum := d.Ticks + other.Ticks
	if sum < d.Ticks {
		return Duration{}, fmt.Errorf("media: duration overflow adding %d+%d", d.Ticks, other.Ticks)
	}
	return Duration{Ticks: sum, Base: d.Base}, nil
}

// Milliseconds converts ticks to whole milliseconds using floor division,
// without floating point.
func (d Duration) Milliseconds() int64 {
	if !d.Base.Valid() || d.Ticks <= 0 {
		return 0
	}
	return d.Ticks * 1000 / int64(d.Base.TicksPerSecond)
}

// ConvertTo expresses the same physical duration on another integral
// clock using exact rational arithmetic. The result is truncated to whole
// ticks of the destination clock.
func (d Duration) ConvertTo(dst TimeBase) (Duration, error) {
	if !d.Base.Valid() || !dst.Valid() {
		return Duration{}, errors.New("media: conversion requires valid time bases")
	}
	if d.Base == dst {
		return d, nil
	}
	ticks, err := mulDiv(d.Ticks, int64(dst.TicksPerSecond), int64(d.Base.TicksPerSecond))
	if err != nil {
		return Duration{}, err
	}
	return Duration{Ticks: ticks, Base: dst}, nil
}

// mulDiv computes a*b/c with an overflow check on the intermediate product.
func mulDiv(a, b, c int64) (int64, error) {
	if c == 0 {
		return 0, errors.New("media: divide by zero in duration conversion")
	}
	if a < 0 || b < 0 {
		return 0, fmt.Errorf("media: negative operand in duration conversion: %d %d", a, b)
	}
	if a != 0 && b > (1<<63-1)/a {
		return 0, fmt.Errorf("media: duration conversion overflow: %d*%d", a, b)
	}
	return a * b / c, nil
}

// CommonClock converts two durations onto the shared clock defined by the
// least common multiple of their tick rates, so callers can compare them
// without lossy rounding.
func CommonClock(a, b TimeBase) (TimeBase, error) {
	if !a.Valid() || !b.Valid() {
		return TimeBase{}, errors.New("media: common clock requires valid time bases")
	}
	x, y := int64(a.TicksPerSecond), int64(b.TicksPerSecond)
	g := gcd(x, y)
	lcm, err := mulDiv(x/g, y, 1)
	if err != nil || lcm > 1<<32-1 {
		return TimeBase{}, fmt.Errorf("media: common clock rate out of range for %s and %s", a, b)
	}
	return TimeBase{TicksPerSecond: uint32(lcm)}, nil
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
