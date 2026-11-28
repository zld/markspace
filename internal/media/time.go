package media

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// NanosPerSecond is the fixed integer time base shared by the whole pipeline.
const NanosPerSecond int64 = 1_000_000_000

// MaxSupportedTimescale bounds manifest timescales so that a single tick can
// never underflow below one nanosecond. Larger values are rejected instead of
// being silently rounded.
const MaxSupportedTimescale uint32 = 1_000_000_000

// ErrUnsupportedFormat marks a container or manifest construct the pipeline
// refuses to approximate instead of merging with a guessed time base.
var ErrUnsupportedFormat = errors.New("unsupported format")

// Timebase is an explicit, positive integer tick rate. All conversions go
// through it; there is no floating point time anywhere in the pipeline.
type Timebase struct {
	Timescale uint32
}

// NewTimebase validates a manifest or sample-entry timescale.
func NewTimebase(timescale uint32, where string) (Timebase, error) {
	if timescale == 0 {
		return Timebase{}, New(StageParse, "timebase", "zero timescale").WithPath(where)
	}
	if timescale > MaxSupportedTimescale {
		return Timebase{}, New(StageParse, "timebase", "timescale %d exceeds supported maximum %d", timescale, MaxSupportedTimescale).WithPath(where)
	}
	return Timebase{Timescale: timescale}, nil
}

// ToNanos converts a tick count into integer nanoseconds, rounding to nearest.
// It reports overflow rather than wrapping.
func (tb Timebase) ToNanos(units int64) (int64, error) {
	if tb.Timescale == 0 {
		return 0, New(StageParse, "timebase", "zero timescale")
	}
	if units < 0 {
		return 0, New(StageParse, "timebase", "negative tick count %d", units)
	}
	whole := units / int64(tb.Timescale)
	rem := units % int64(tb.Timescale)
	if whole > math.MaxInt64/NanosPerSecond {
		return 0, New(StageParse, "timebase", "tick count %d overflows nanoseconds at timescale %d", units, tb.Timescale)
	}
	ns := whole * NanosPerSecond
	// rem < timescale <= 1e9, so rem*NanosPerSecond stays below 1e18 and
	// cannot overflow int64. Round half up to keep error below one nanosecond.
	scale := int64(tb.Timescale)
	frac := (rem*NanosPerSecond + scale/2) / scale
	if ns > math.MaxInt64-frac {
		return 0, New(StageParse, "timebase", "tick count %d overflows nanoseconds at timescale %d", units, tb.Timescale)
	}
	return ns + frac, nil
}

// DurationNanos is the convenience conversion for a Duration.
func (tb Timebase) DurationNanos(d time.Duration) (int64, error) {
	return tb.ToNanos(int64(d))
}

// Segment is one manifest segment with an integer-nanosecond start and
// duration, both derived from the same explicit time base.
type Segment struct {
	Index    int
	Start    int64 // ns
	Duration int64 // ns
}

// End returns the exclusive end boundary in nanoseconds.
func (s Segment) End() int64 { return s.Start + s.Duration }

// MaxBoundaryDrift is the hard limit for cumulative segment-boundary error.
const MaxBoundaryDrift int64 = 1_000_000 // 1 ms in ns

// BuildTimeline accumulates segment durations into boundaries and verifies
// that the accumulated end never drifts from the declared total by more than
// one millisecond. A nil error guarantees the boundary invariant holds.
func BuildTimeline(durations []int64, tb Timebase, declaredTotal int64) ([]Segment, error) {
	segments := make([]Segment, 0, len(durations))
	var cursor int64
	for i, d := range durations {
		if d < 0 {
			return nil, New(StageParse, "timeline", "negative duration %d at segment %d", d, i).WithSegment(i)
		}
		end := cursor + d
		if end < cursor {
			return nil, New(StageParse, "timeline", "cumulative nanosecond overflow at segment %d", i).WithSegment(i)
		}
		segments = append(segments, Segment{Index: i, Start: cursor, Duration: d})
		cursor = end
	}
	if declaredTotal >= 0 {
		drift := cursor - declaredTotal
		if drift < 0 {
			drift = -drift
		}
		if drift > MaxBoundaryDrift {
			return nil, New(StageParse, "timeline", "boundary drift %dns exceeds %dns limit (accumulated %dns, declared %dns)", drift, MaxBoundaryDrift, cursor, declaredTotal)
		}
	}
	return segments, nil
}

// CheckDrift validates an observed boundary against an expected one.
func CheckDrift(expectedNs, observedNs int64, what string, segment ...int) error {
	d := observedNs - expectedNs
	if d < 0 {
		d = -d
	}
	if d > MaxBoundaryDrift {
		e := New(StageParse, what, "drift %dns exceeds %dns limit", d, MaxBoundaryDrift)
		if len(segment) > 0 {
			e = e.WithSegment(segment[0])
		}
		return e
	}
	return nil
}

// NanosToDuration converts integer nanoseconds to a time.Duration for display.
func NanosToDuration(ns int64) time.Duration { return time.Duration(ns) }

// FormatNanos renders integer nanoseconds as seconds with millisecond detail.
func FormatNanos(ns int64) string {
	sign := ""
	if ns < 0 {
		sign = "-"
		ns = -ns
	}
	return fmt.Sprintf("%s%d.%03ds", sign, ns/NanosPerSecond, (ns%NanosPerSecond)/1_000_000)
}
