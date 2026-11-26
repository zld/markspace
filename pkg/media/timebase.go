// Package media contains the integer-tick clock and structured fault model
// shared by the MP4 parser, indexer, pipeline, and rewriter.
package media

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Timebase is an MP4 media time base. Timescale is the number of clock ticks
// that occur in one second.
type Timebase struct {
	Timescale uint32 `json:"timescale"`
}

// Valid reports whether the time base can be used for conversions.
func (tb Timebase) Valid() bool { return tb.Timescale > 0 }

// Ticks converts a duration to integer media ticks.
func (tb Timebase) Ticks(d time.Duration) (int64, error) {
	if !tb.Valid() {
		return 0, errors.New("media: invalid zero timescale")
	}
	sec := int64(d / time.Second)
	rem := int64(d % time.Second)
	if sec > math.MaxInt64/int64(tb.Timescale) {
		return 0, fmt.Errorf("media: duration %s overflows timescale %d", d, tb.Timescale)
	}
	ticks := sec * int64(tb.Timescale)
	frac := rem * int64(tb.Timescale) / int64(time.Second)
	if ticks > math.MaxInt64-frac {
		return 0, fmt.Errorf("media: duration %s overflows ticks", d)
	}
	return ticks + frac, nil
}

// Duration converts media ticks to a Go duration. It is intentionally integer
// based so parse and rewrap decisions use exactly the same clock.
func (tb Timebase) Duration(ticks int64) time.Duration {
	if !tb.Valid() {
		return 0
	}
	sec := ticks / int64(tb.Timescale)
	rem := ticks % int64(tb.Timescale)
	return time.Duration(sec)*time.Second +
		time.Duration(rem)*time.Second/time.Duration(tb.Timescale)
}

// Clock is a track clock with an optional start offset in the same tick unit.
type Clock struct {
	Timebase   Timebase `json:"timebase"`
	StartTicks int64    `json:"start_ticks"`
}

// PTS applies a composition-time offset to a decode timestamp while rejecting
// signed integer overflow.
func (c Clock) PTS(dts, compositionOffset int64) (int64, error) {
	if compositionOffset > 0 && dts > math.MaxInt64-compositionOffset {
		return 0, fmt.Errorf("media: PTS overflow dts=%d ctts=%d", dts, compositionOffset)
	}
	if compositionOffset < 0 && dts < math.MinInt64-compositionOffset {
		return 0, fmt.Errorf("media: PTS underflow dts=%d ctts=%d", dts, compositionOffset)
	}
	return dts + compositionOffset, nil
}

// Advance returns base+duration with overflow detection.
func Advance(base int64, delta uint32) (int64, error) {
	d := int64(delta)
	if base > math.MaxInt64-d {
		return 0, fmt.Errorf("media: timestamp overflow at %d + %d", base, delta)
	}
	return base + d, nil
}

// EditListEntry is one edts/elst entry expressed in movie and media ticks.
type EditListEntry struct {
	SegmentDuration uint64 `json:"segment_duration"`
	MediaTime       int64  `json:"media_time"`
	MediaRate       int16  `json:"media_rate"`
}

// Keyframe is a sync sample position in sample-number order.
type Keyframe struct {
	SampleNumber uint32 `json:"sample_number"`
	Offset       int64  `json:"offset"`
}
