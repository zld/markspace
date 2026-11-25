// Package mediakernel implements an offline media processing kernel: packet
// timeline mapping, bounded streaming pipelines, content/container digests,
// incremental indexing, spec/quality metrics and versioned manifest migration.
package mediakernel

import (
	"math"
	"time"
)

// Default media clock rate shared by dialogue, subtitle and sound-effect checks.
// All packet durations are normalized onto this single time base so the three
// asset kinds can be compared against one another.
const (
	DefaultClockRate uint32 = 48000
	nanosPerSecond          = 1_000_000_000
)

// Clock converts media-timestamp units (ticks at Rate) to time.Duration and
// milliseconds. A zero-value Clock uses DefaultClockRate.
type Clock struct {
	Rate uint32
}

// NewClock returns a clock at rate; rate 0 falls back to DefaultClockRate.
func NewClock(rate uint32) Clock {
	if rate == 0 {
		rate = DefaultClockRate
	}
	return Clock{Rate: rate}
}

// TicksToDuration converts sample/timestamp ticks to a duration.
func (c Clock) TicksToDuration(ticks int64) time.Duration {
	rate := c.effectiveRate()
	return time.Duration(ticks) * time.Second / time.Duration(rate)
}

// DurationToTicks converts a duration back to media ticks (truncated).
func (c Clock) DurationToTicks(d time.Duration) int64 {
	rate := int64(c.effectiveRate())
	return d.Nanoseconds() * rate / nanosPerSecond
}

// TicksToMillis converts ticks to milliseconds as a float so sub-millisecond
// offsets are preserved in JSON alignment reports.
func (c Clock) TicksToMillis(ticks int64) float64 {
	return float64(ticks) * 1000.0 / float64(c.effectiveRate())
}

// MillisToTicks converts milliseconds back to media ticks.
func (c Clock) MillisToTicks(ms float64) int64 {
	return int64(math.Round(ms * float64(c.effectiveRate()) / 1000.0))
}

func (c Clock) effectiveRate() uint32 {
	if c.Rate == 0 {
		return DefaultClockRate
	}
	return c.Rate
}

// TimeRange is an interval on the media clock. End is exclusive.
type TimeRange struct {
	StartTicks int64 `json:"start_ticks"`
	EndTicks   int64 `json:"end_ticks"`
}

// Duration returns the range length on the media clock.
func (r TimeRange) Duration() time.Duration {
	if r.EndTicks <= r.StartTicks {
		return 0
	}
	return NewClock(DefaultClockRate).TicksToDuration(r.EndTicks - r.StartTicks)
}

// StartMillis / EndMillis render the range in milliseconds for JSON reports.
func (r TimeRange) StartMillis() float64 {
	return NewClock(DefaultClockRate).TicksToMillis(r.StartTicks)
}
func (r TimeRange) EndMillis() float64 { return NewClock(DefaultClockRate).TicksToMillis(r.EndTicks) }

// Contains reports whether tick falls inside the half-open range.
func (r TimeRange) Contains(tick int64) bool {
	return tick >= r.StartTicks && tick < r.EndTicks
}

// Timeline maps an ordered packet stream onto the shared media clock.
// Duplicate packets never advance the clock; lost packets leave a recorded gap
// range but no audio content is synthesized.
type Timeline struct {
	clock       Clock
	cursor      int64
	lastSeq     uint16
	hasLast     bool
	lastSamples int64
}

// NewTimeline starts a timeline at tick zero on the given clock.
func NewTimeline(clock Clock) *Timeline {
	return &Timeline{clock: clock}
}

// Clock returns the timeline's media clock.
func (t *Timeline) Clock() Clock { return t.clock }

// Cursor returns the current media tick (total unique samples mapped so far).
func (t *Timeline) Cursor() int64 { return t.cursor }

// Advance maps one unique (non-duplicate) packet carrying validSamples and
// returns its half-open time range. Callers must first exclude duplicates via
// the gap tracker.
func (t *Timeline) Advance(validSamples int64) TimeRange {
	r := TimeRange{StartTicks: t.cursor, EndTicks: t.cursor + validSamples}
	t.cursor += validSamples
	t.lastSamples = validSamples
	return r
}

// NoteSeq records the observed sequence number so callers can size gap ranges.
func (t *Timeline) NoteSeq(seq uint16) {
	t.lastSeq = seq
	t.hasLast = true
}

// HasLastSeq reports whether any sequence number has been observed.
func (t *Timeline) HasLastSeq() bool { return t.hasLast }

// LastSeq returns the most recently observed sequence number.
func (t *Timeline) LastSeq() uint16 { return t.lastSeq }

// LastPacketSamples returns the sample count of the previous unique packet,
// used to estimate the missing duration of a gap before more data arrives.
func (t *Timeline) LastPacketSamples() int64 { return t.lastSamples }

// Reserve marks missing samples for a lost packet without producing content.
func (t *Timeline) Reserve(missingSamples int64) TimeRange {
	r := TimeRange{StartTicks: t.cursor, EndTicks: t.cursor + missingSamples}
	t.cursor += missingSamples
	return r
}
