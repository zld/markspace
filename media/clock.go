// Package media contains the shared time base and structured error model used
// by ingestion, codec analysis, indexing, damage scanning and the pipeline.
package media

import (
	"fmt"
	"time"
)

// Timebase is the rational duration of one clock tick: Numerator/Denominator
// seconds. For a 48 kHz clock it is {Numerator: 1, Denominator: 48000}.
type Timebase struct {
	Numerator   int64 `json:"numerator"`
	Denominator int64 `json:"denominator"`
}

// Common time bases used by audio packets and MP4 media timelines.
var (
	Timebase48k   = Timebase{Numerator: 1, Denominator: 48000}
	Timebase90k   = Timebase{Numerator: 1, Denominator: 90000}
	TimebaseMP4k  = Timebase{Numerator: 1, Denominator: 1000}
	TimebaseNanos = Timebase{Numerator: 1, Denominator: int64(time.Second)}
)

// Validate reports whether the rational time base is usable.
func (tb Timebase) Validate() error {
	if tb.Numerator <= 0 || tb.Denominator <= 0 {
		return NewError("media", CodeInvalidTimebase,
			fmt.Sprintf("time base must have positive numerator and denominator, got %d/%d",
				tb.Numerator, tb.Denominator), nil)
	}
	return nil
}

// Duration converts a tick count in this time base to time.Duration.
func (tb Timebase) Duration(ticks int64) time.Duration {
	if err := tb.Validate(); err != nil {
		return 0
	}
	if ticks == 0 {
		return 0
	}
	// Nanoseconds are the representation used by time.Duration.
	nanos := float64(ticks) * float64(tb.Numerator) * float64(time.Second) / float64(tb.Denominator)
	return time.Duration(nanos)
}

// Ticks converts a duration to whole ticks in this time base.
func (tb Timebase) Ticks(d time.Duration) int64 {
	if err := tb.Validate(); err != nil {
		return 0
	}
	return int64(float64(d) * float64(tb.Denominator) /
		(float64(time.Second) * float64(tb.Numerator)))
}

// Convert maps ticks from tb into dst without changing the represented time.
func (tb Timebase) Convert(ticks int64, dst Timebase) (int64, error) {
	if err := tb.Validate(); err != nil {
		return 0, err
	}
	if err := dst.Validate(); err != nil {
		return 0, err
	}
	// Intermediate multiplication is done in floating point because all local
	// media timelines are small; callers round only at a boundary.
	nanos := float64(ticks) * float64(tb.Numerator) * float64(time.Second) / float64(tb.Denominator)
	out := int64(nanos * float64(dst.Denominator) /
		(float64(time.Second) * float64(dst.Numerator)))
	return out, nil
}

// Timestamp is an integer tick value together with its time base.
type Timestamp struct {
	Ticks    int64    `json:"ticks"`
	Timebase Timebase `json:"timebase"`
}

// Time returns the timestamp as an absolute time.Duration from zero.
func (t Timestamp) Time() time.Duration { return t.Timebase.Duration(t.Ticks) }

// TimeRange is a half-open [Start, End) interval on one time base.
type TimeRange struct {
	Start Timestamp `json:"start"`
	End   Timestamp `json:"end"`
}

// NewRange constructs and validates a half-open tick interval. Invalid
// timestamps are returned as structured errors rather than replaced by zero.
func NewRange(tb Timebase, start, durationTicks int64) (TimeRange, error) {
	if err := tb.Validate(); err != nil {
		return TimeRange{}, err
	}
	if start < 0 {
		return TimeRange{}, NewError("media", CodeTimestampNegative,
			fmt.Sprintf("timestamp %d is negative", start), nil).
			WithOffset(start)
	}
	if durationTicks < 0 {
		return TimeRange{}, NewError("media", CodeTimestampInvalid,
			fmt.Sprintf("duration %d is negative", durationTicks), nil)
	}
	end := start + durationTicks
	if end < start {
		return TimeRange{}, NewError("media", CodeTimestampOverflow,
			fmt.Sprintf("range start=%d duration=%d overflows", start, durationTicks), nil)
	}
	return TimeRange{
		Start: Timestamp{Ticks: start, Timebase: tb},
		End:   Timestamp{Ticks: end, Timebase: tb},
	}, ValidateRange(TimeRange{
		Start: Timestamp{Ticks: start, Timebase: tb},
		End:   Timestamp{Ticks: end, Timebase: tb},
	})
}

// ValidateRange checks positivity and end-before-start ordering.
func ValidateRange(r TimeRange) error {
	if err := r.Start.Timebase.Validate(); err != nil {
		return err
	}
	if r.Start.Ticks < 0 {
		return NewError("media", CodeTimestampNegative,
			fmt.Sprintf("range start %d is negative", r.Start.Ticks), nil)
	}
	if r.End.Ticks < r.Start.Ticks {
		return NewError("media", CodeTimestampNonMonotonic,
			fmt.Sprintf("range end %d precedes start %d", r.End.Ticks, r.Start.Ticks), nil).
			WithRange(&r)
	}
	return nil
}

// Duration returns the range duration in its own time base.
func (r TimeRange) Duration() time.Duration {
	return r.Start.Timebase.Duration(r.End.Ticks - r.Start.Ticks)
}

// CheckAdjacent verifies that next does not begin before prev ends. A later
// start is permitted and represents a gap; overlap is a timestamp error.
func CheckAdjacent(prevEnd, nextStart Timestamp) error {
	next, err := nextStart.Timebase.Convert(nextStart.Ticks, prevEnd.Timebase)
	if err != nil {
		return err
	}
	if next < prevEnd.Ticks {
		r, _ := NewRange(prevEnd.Timebase, next, prevEnd.Ticks-next)
		return NewError("media", CodeTimestampNonMonotonic,
			fmt.Sprintf("next timestamp %d precedes previous end %d", next, prevEnd.Ticks), nil).
			WithRange(&r)
	}
	return nil
}
