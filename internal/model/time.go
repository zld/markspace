// Package model defines the shared time base, bounded integer helpers,
// error model and media types used by every subsystem.
package model

import (
	"errors"
	"math/bits"
)

// PlayTime is a monotonic presentation time in nanoseconds on the unified
// playout timeline. Negative values are invalid; the zero value is the
// timeline origin.
type PlayTime int64

// MaxPlayTime bounds every timeline calculation in the kernel.
const MaxPlayTime PlayTime = 1<<62 - 1

// TimeScale is a track or manifest clock: ticks per second. Zero is
// rejected by every parser.
type TimeScale uint32

// Valid reports whether the time scale is usable.
func (t TimeScale) Valid() bool { return t > 0 }

// ErrOverflow is returned by bounded arithmetic helpers.
var ErrOverflow = errors.New("bounded integer overflow")

// Scale converts a duration in ticks (in time scale ts) to nanoseconds,
// rejecting fields that would overflow int64.
func Scale(ticks int64, ts TimeScale) (PlayTime, error) {
	if !ts.Valid() {
		return 0, errors.New("zero or invalid time scale")
	}
	if ticks < 0 {
		return 0, errors.New("negative tick count")
	}
	hi, lo := bits.Mul64(uint64(ticks), 1_000_000_000)
	q, rem := bits.Div64(hi, lo, uint64(ts))
	if hi != 0 || q > uint64(MaxPlayTime) || rem != 0 {
		// Remainder is handled by ScaleRound; exact scaling must not lose
		// information, otherwise the manifest cannot round-trip.
		return 0, ErrOverflow
	}
	return PlayTime(q), nil
}

// ScaleRound converts ticks to nanoseconds rounding half away from zero.
// Used for media sample times where exact divisibility is not guaranteed.
func ScaleRound(ticks int64, ts TimeScale) (PlayTime, error) {
	if !ts.Valid() {
		return 0, errors.New("zero or invalid time scale")
	}
	if ticks < 0 {
		return 0, errors.New("negative tick count")
	}
	num, ok := checkedMul64(uint64(ticks), 1_000_000_000)
	if !ok {
		return 0, ErrOverflow
	}
	num, ok = checkedAdd64(num, uint64(ts)/2)
	if !ok {
		return 0, ErrOverflow
	}
	q := num / uint64(ts)
	if q > uint64(MaxPlayTime) {
		return 0, ErrOverflow
	}
	return PlayTime(q), nil
}

// Unscale converts nanoseconds back to ticks for round-trip verification.
func Unscale(ns PlayTime, ts TimeScale) (int64, error) {
	if !ts.Valid() {
		return 0, errors.New("zero or invalid time scale")
	}
	if ns < 0 {
		return 0, errors.New("negative play time")
	}
	num, ok := checkedMul64(uint64(ns), uint64(ts))
	if !ok {
		return 0, ErrOverflow
	}
	num, ok = checkedAdd64(num, 500_000_000)
	if !ok {
		return 0, ErrOverflow
	}
	return int64(num / 1_000_000_000), nil
}

// Add returns a+b, rejecting overflow and negative results.
func Add(a, b PlayTime) (PlayTime, error) {
	s, ok := checkedAdd64(uint64(a), uint64(b))
	if !ok || s > uint64(MaxPlayTime) || a < 0 || b < 0 {
		return 0, ErrOverflow
	}
	return PlayTime(s), nil
}

func checkedAdd64(a, b uint64) (uint64, bool) {
	s, carry := bits.Add64(a, b, 0)
	return s, carry == 0
}

func checkedMul64(a, b uint64) (uint64, bool) {
	hi, lo := bits.Mul64(a, b)
	return lo, hi == 0
}

// RTP helpers (RFC 3550 §3.1): sequence numbers and timestamps are
// unsigned modular counters. WrapDiff gives the signed distance from a to b.

// RTPSeqDiff returns the modular difference b-a for 16-bit sequence numbers.
func RTPSeqDiff(a, b uint16) int32 {
	return int32(int16(b - a))
}

// RTPTSDiff returns the modular difference b-a for 32-bit RTP timestamps.
func RTPTSDiff(a, b uint32) int64 {
	return int64(int32(b - a))
}
