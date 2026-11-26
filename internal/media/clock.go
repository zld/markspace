package media

import (
	"math"
	"math/bits"
	"time"
)

// Nanosecond is the project-wide unified timestamp unit.
type Nanosecond int64

const (
	NanosecondZero Nanosecond = 0
	NanosecondMax  Nanosecond = Nanosecond(math.MaxInt64)
)

// Timebase describes a media tick rate in ticks per second.
type Timebase struct {
	Numerator   uint32 `json:"numerator,omitempty"`
	Denominator uint32 `json:"denominator,omitempty"`
	TicksPerSec uint32 `json:"ticks_per_sec"`
}

// MillisecondTimebase is the common Matroska timecode scale when
// TimecodeScale equals one million nanoseconds.
func MillisecondTimebase() Timebase { return Timebase{TicksPerSec: 1000} }

// CheckedAdd returns a+b or a structured overflow error.
func CheckedAdd(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, NewError(StageTimeline, -1, "", ErrTimeOverflow)
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, NewError(StageTimeline, -1, "", ErrTimeOverflow)
	}
	return a + b, nil
}

// ScaleTicks converts an unsigned tick count at ticksPerSecond to nanoseconds.
// It uses 128-bit multiplication checks before division.
func ScaleTicks(ticks uint64, ticksPerSecond uint32) (int64, error) {
	if ticksPerSecond == 0 {
		return 0, NewError(StageTimeline, -1, "", ErrTimeOverflow).WithKind("zero_timebase")
	}
	hi, lo := bits.Mul64(ticks, uint64(time.Second))
	if hi != 0 {
		return 0, NewError(StageTimeline, -1, "", ErrTimeOverflow).WithKind("tick_multiplication")
	}
	ns := lo / uint64(ticksPerSecond)
	if ns > math.MaxInt64 {
		return 0, NewError(StageTimeline, -1, "", ErrTimeOverflow).WithKind("nanosecond_range")
	}
	return int64(ns), nil
}

// ScaleSignedTicks converts a signed tick delta. It is intended for block
// relative timecodes and RTP deltas after wrap expansion.
func ScaleSignedTicks(ticks int64, ticksPerSecond uint32) (int64, error) {
	if ticks >= 0 {
		return ScaleTicks(uint64(ticks), ticksPerSecond)
	}
	if ticks == math.MinInt64 {
		// Magnitude cannot be represented as a positive int64, but uint64 can.
		ns, err := scaleMagnitude(uint64(math.MaxInt64)+1, ticksPerSecond)
		if err != nil {
			return 0, err
		}
		if ns > uint64(math.MaxInt64)+1 {
			return 0, overflow()
		}
		return -int64(ns), nil
	}
	ns, err := ScaleTicks(uint64(-ticks), ticksPerSecond)
	if err != nil {
		return 0, err
	}
	return -ns, nil
}

func scaleMagnitude(ticks uint64, ticksPerSecond uint32) (uint64, error) {
	if ticksPerSecond == 0 {
		return 0, overflow()
	}
	hi, lo := bits.Mul64(ticks, uint64(time.Second))
	if hi != 0 {
		return 0, overflow()
	}
	return lo / uint64(ticksPerSecond), nil
}

func overflow() *Error {
	return NewError(StageTimeline, -1, "", ErrTimeOverflow)
}

// ScaleMatroskaTimecode converts timecode units where one tick is
// timecodeScaleNS nanoseconds. Cluster and block timecodes share this scale.
func ScaleMatroskaTimecode(ticks int64, timecodeScaleNS uint64) (int64, error) {
	if timecodeScaleNS == 0 {
		return 0, NewError(StageEBML, -1, "", ErrTimeOverflow).WithKind("zero_timecode_scale")
	}
	if ticks < 0 {
		mag := uint64(-ticks)
		if ticks == math.MinInt64 {
			mag = uint64(math.MaxInt64) + 1
		}
		hi, lo := bits.Mul64(mag, timecodeScaleNS)
		if hi != 0 || lo > uint64(math.MaxInt64)+1 {
			return 0, NewError(StageEBML, -1, "", ErrTimeOverflow).WithKind("negative_timecode_scale")
		}
		return -int64(lo), nil
	}
	hi, lo := bits.Mul64(uint64(ticks), timecodeScaleNS)
	if hi != 0 || lo > math.MaxInt64 {
		return 0, NewError(StageEBML, -1, "", ErrTimeOverflow).WithKind("timecode_scale")
	}
	return int64(lo), nil
}

// DurationMS converts a nanosecond duration to integer milliseconds.
func DurationMS(ns int64) int64 { return ns / int64(time.Millisecond) }
