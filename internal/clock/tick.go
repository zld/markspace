// Package clock defines the project-wide integer rational time base.
//
// Every timeline value that crosses a package boundary is expressed as a
// Tick count on the master 90 kHz clock. Conversions between foreign time
// scales (MP4 mdhd, RTP, millisecond statistics) are exact integer rational
// operations and return an error on overflow instead of silently wrapping.
package clock

import (
	"errors"
	"math"
)

// MasterHZ is the common time base in ticks per second. 90 kHz matches the
// MPEG-2/AVC RTP video clock and divides the common AAC scales evenly.
const (
	MasterHZ uint64 = 90000
	maxU64          = math.MaxUint64
	maxI64          = uint64(math.MaxInt64)
)

// Scale is an integer rational tick frequency: the clock runs at
// Num/Den ticks per second. Den == 1 gives the common MP4/RTP "timescale"
// convention; a fractional Den supports NTSC-derived clocks (30000/1001).
// Num must be positive; zero is rejected on use.
type Scale struct {
	Num uint64 // ticks
	Den uint64 // per this many seconds
}

// NewScale validates and returns a rational scale (Num/Den ticks per second).
func NewScale(num, den uint64) (Scale, error) {
	if num == 0 || den == 0 {
		return Scale{}, errors.New("clock: scale must have positive numerator and denominator")
	}
	return Scale{Num: num, Den: den}, nil
}

// mul64 returns a*b and whether it overflows uint64.
func mul64(a, b uint64) (uint64, bool) {
	if a != 0 && b > maxU64/a {
		return 0, false
	}
	return a * b, true
}

func add64(a, b uint64) (uint64, bool) {
	if b > maxU64-a {
		return 0, false
	}
	return a + b, true
}

// ToMaster converts value ticks expressed in scale s to master ticks,
// rounding to the nearest master tick (half away from zero).
func (s Scale) ToMaster(value uint64) (uint64, error) {
	if s.Num == 0 {
		return 0, errors.New("clock: zero numerator scale")
	}
	num, ok := mul64(value, MasterHZ)
	if !ok {
		return 0, errors.New("clock: conversion overflow")
	}
	num, ok = mul64(num, s.Den)
	if !ok {
		return 0, errors.New("clock: conversion overflow")
	}
	return (num + s.Num/2) / s.Num, nil
}

// FromMaster converts master ticks into scale s, rounding to the nearest tick.
func (s Scale) FromMaster(master uint64) (uint64, error) {
	if s.Num == 0 {
		return 0, errors.New("clock: zero numerator scale")
	}
	denFreq, ok := mul64(MasterHZ, s.Den)
	if !ok {
		return 0, errors.New("clock: conversion overflow")
	}
	num, ok := mul64(master, s.Num)
	if !ok {
		return 0, errors.New("clock: conversion overflow")
	}
	return (num + denFreq/2) / denFreq, nil
}

// Convert re-expresses value from scale from to scale to without leaving
// integer arithmetic.
func Convert(value uint64, from, to Scale) (uint64, error) {
	m, err := from.ToMaster(value)
	if err != nil {
		return 0, err
	}
	return to.FromMaster(m)
}

// Millis is the convenience scale for millisecond statistics.
var Millis = Scale{Num: 1000, Den: 1}

// ToMillis converts master ticks to whole milliseconds (rounded).
func ToMillis(master uint64) (uint64, error) { return Millis.FromMaster(master) }

// FromMillis converts whole milliseconds to master ticks.
func FromMillis(ms uint64) (uint64, error) { return Millis.ToMaster(ms) }

// AbsDiff returns |a-b| on master ticks; inputs are non-negative so the
// subtraction never underflows.
func AbsDiff(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

// Add returns a+b or an error on uint64 overflow.
func Add(a, b uint64) (uint64, error) {
	v, ok := add64(a, b)
	if !ok {
		return 0, errors.New("clock: addition overflow")
	}
	return v, nil
}

// GCD returns the greatest common divisor (Euclid).
func GCD(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// UnwrapTS reconstructs a monotonic 64-bit counter from successive unsigned
// width-bit samples, tolerating one wraparound per step. The first sample is
// returned as-is. prev is the previously unwrapped value.
//
// width must be in [1,64]. Forward motion is assumed; the correction only
// applies when a sample lies more than half the modulus behind the previous
// one, which is the standard RTP/serial-number rule (RFC 3550 appendix A).
func UnwrapTS(prev, sample uint64, width uint) (uint64, error) {
	if width == 0 || width > 64 {
		return 0, errors.New("clock: unwrap width out of range")
	}
	if width == 64 {
		return sample, nil
	}
	mod := uint64(1) << width
	half := mod / 2
	base := prev - prev%mod
	candidate := base + sample
	switch {
	case candidate+half <= prev && candidate < maxU64-mod+1:
		candidate += mod
	case candidate > prev+half && candidate >= mod:
		candidate -= mod
	}
	return candidate, nil
}

// SeqDistance returns the signed forward distance from a to b for 16-bit
// RTP sequence numbers, in (-32768, 32768]. Positive means b is newer.
func SeqDistance(a, b uint16) int {
	d := int(int16(b - a))
	return d
}

// CheckI64 fails when v cannot be stored in a signed int64 field (mp4 boxes).
func CheckI64(v uint64) error {
	if v > maxI64 {
		return errors.New("clock: value exceeds int64 range")
	}
	return nil
}
