// Package model defines the unified timebase and shared error model
// used by every stage of the media rebuild kernel.
package model

import (
	"errors"
	"fmt"
	"math"
)

// MediaTime is a position on the unified media timeline, in nanoseconds.
// All tracks, RTP/RTCP mappings and container timestamps are converted to
// this single timebase so downstream results can be compared and replayed.
type MediaTime int64

// SampleDuration is a duration expressed in nanoseconds on the unified line.
type SampleDuration int64

// Timebase is a track timebase: Num/Den seconds per tick... kept as a
// rational so conversion is exact for common rates (90000, 48000, 1000).
type Timebase struct {
	Num int64 // ticks per ...
	Den int64 // ... Num/Den ticks per second (e.g. 90000/1)
}

// Timescale returns a Timebase of n ticks per second.
func Timescale(n int64) Timebase { return Timebase{Num: n, Den: 1} }

var (
	ErrInvalidTimebase = errors.New("model: invalid timebase")
	ErrOverflow        = errors.New("model: time conversion overflow")
)

// Valid reports whether the timebase is usable.
func (tb Timebase) Valid() bool { return tb.Num > 0 && tb.Den > 0 }

// ToMedia converts ticks in this timebase to the unified nanosecond line.
// It checks for int64 overflow and reports it instead of wrapping.
func (tb Timebase) ToMedia(ticks int64) (MediaTime, error) {
	if !tb.Valid() {
		return 0, ErrInvalidTimebase
	}
	// ns = ticks * Den * 1e9 / Num, computed with overflow checks.
	num := tb.Den * int64(timeNanosPerSec)
	if ticks != 0 && abs64(ticks) > math.MaxInt64/abs64(num) {
		return 0, fmt.Errorf("%w: ticks=%d num=%d", ErrOverflow, ticks, num)
	}
	prod := ticks * num
	q := prod / tb.Num
	return MediaTime(q), nil
}

// FromMedia converts a unified-timeline position back to ticks, rounding
// toward zero. Overflow is reported.
func (tb Timebase) FromMedia(t MediaTime) (int64, error) {
	if !tb.Valid() {
		return 0, ErrInvalidTimebase
	}
	den := tb.Den * int64(timeNanosPerSec)
	if int64(t) != 0 && abs64(int64(t)) > math.MaxInt64/tb.Num {
		return 0, fmt.Errorf("%w: t=%d", ErrOverflow, int64(t))
	}
	return int64(t) * tb.Num / den, nil
}

// TickDuration returns the duration of one tick on the unified line.
func (tb Timebase) TickDuration() (SampleDuration, error) {
	t, err := tb.ToMedia(1)
	return SampleDuration(t), err
}

const timeNanosPerSec = 1_000_000_000

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
