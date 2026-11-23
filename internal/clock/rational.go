// Package clock provides exact, integer-only rational timebase conversion.
//
// All timeline math is performed with integers; no floating point value ever
// participates in an accumulated timestamp. Conversions that cannot be
// represented in an int64 are rejected instead of silently wrapping.
package clock

import (
	"errors"
	"math"
	"math/big"
)

var (
	ErrTimescale   = errors.New("clock: timescale must be positive")
	ErrOverflow    = errors.New("clock: conversion overflows int64")
	ErrNegative    = errors.New("clock: duration must not be negative")
	ErrDenominator = errors.New("clock: denominator must be positive")
)

// Rational is an exact ratio Num/Den. Den is always positive.
type Rational struct {
	Num int64
	Den int64
}

// New builds a reduced rational. A negative denominator is normalised; a zero
// denominator is rejected.
func New(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, ErrDenominator
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(num, den)
	if g != 0 {
		num /= g
		den /= g
	}
	return Rational{Num: num, Den: den}, nil
}

func gcd(a, b int64) int64 {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// Rescale converts ticks from the from timescale to the to timescale using
// exact integer arithmetic. The result is truncated toward zero, matching the
// behaviour of dividing two signed integers. Overflow is reported.
func Rescale(ticks int64, from, to uint32) (int64, error) {
	if from == 0 || to == 0 {
		return 0, ErrTimescale
	}
	x := new(big.Int).SetInt64(ticks)
	x.Mul(x, new(big.Int).SetUint64(uint64(to)))
	x.Quo(x, new(big.Int).SetUint64(uint64(from)))
	if !x.IsInt64() {
		return 0, ErrOverflow
	}
	return x.Int64(), nil
}

// Duration validates and returns end-start. A negative duration is rejected.
func Duration(start, end int64) (int64, error) {
	if end < start {
		return 0, ErrNegative
	}
	// Both values are non-negative media times in supported inputs, but guard
	// the subtraction anyway.
	if start > 0 && end > math.MaxInt64 {
		return 0, ErrOverflow
	}
	return end - start, nil
}

// Mapping binds one track timebase to the shared timeline timebase.
// SourceStart is the first decode time declared by the track (tfdt); a
// non-zero value is preserved, not shifted to zero.
type Mapping struct {
	TrackID           uint32 `json:"track_id"`
	SourceTimescale   uint32 `json:"source_timescale"`
	TimelineTimescale uint32 `json:"timeline_timescale"`
	SourceStart       int64  `json:"source_start"`
	TimelineStart     int64  `json:"timeline_start"`
}

// NewMapping records the non-zero start and its mapped timeline position.
func NewMapping(trackID uint32, sourceTimescale, timelineTimescale uint32, sourceStart int64) (Mapping, error) {
	if sourceTimescale == 0 || timelineTimescale == 0 {
		return Mapping{}, ErrTimescale
	}
	if sourceStart < 0 {
		return Mapping{}, ErrNegative
	}
	mapped, err := Rescale(sourceStart, sourceTimescale, timelineTimescale)
	if err != nil {
		return Mapping{}, err
	}
	return Mapping{
		TrackID:           trackID,
		SourceTimescale:   sourceTimescale,
		TimelineTimescale: timelineTimescale,
		SourceStart:       sourceStart,
		TimelineStart:     mapped,
	}, nil
}

// ToTimeline converts an absolute source tick into timeline ticks.
func (m Mapping) ToTimeline(ticks int64) (int64, error) {
	if ticks < 0 {
		return 0, ErrNegative
	}
	return Rescale(ticks, m.SourceTimescale, m.TimelineTimescale)
}

// DurationToTimeline converts a source duration into timeline ticks.
func (m Mapping) DurationToTimeline(ticks int64) (int64, error) {
	if ticks < 0 {
		return 0, ErrNegative
	}
	return Rescale(ticks, m.SourceTimescale, m.TimelineTimescale)
}

// NonZeroStart reports whether the source track starts after media time zero.
func (m Mapping) NonZeroStart() bool {
	return m.SourceStart != 0
}
