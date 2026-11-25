// Package mediaclock is the single integer time base for the pipeline.
//
// All positions are absolute integer PCM sample frames (one frame contains
// one sample per channel). Conversions to nanoseconds use integer multiply
// and divide only, so long scans never accumulate floating-point error.
package mediaclock

import (
	"time"

	"assetcheck/internal/errmodel"
)

// Clock binds frame numbers to a fixed sample rate.
type Clock struct {
	rate int
}

// New returns a clock for rate Hz.
func New(rate int) (*Clock, error) {
	if rate <= 0 {
		return nil, errmodel.New(errmodel.ErrBadSampleRate, "sample rate must be positive, got %d", rate)
	}
	return &Clock{rate: rate}, nil
}

// Rate returns the sample rate in Hz.
func (c *Clock) Rate() int { return c.rate }

// FramesToNanos converts an absolute frame count to nanoseconds using exact
// integer scaling (frames * 1e9 / rate).
func (c *Clock) FramesToNanos(frames int64) int64 {
	if frames <= 0 {
		return 0
	}
	return frames * int64(time.Second) / int64(c.rate)
}

// NanosToFramesFloor converts nanoseconds to frames, truncating toward zero.
func (c *Clock) NanosToFramesFloor(ns int64) int64 {
	if ns <= 0 {
		return 0
	}
	return ns * int64(c.rate) / int64(time.Second)
}

// FrameDurationNanos returns the duration of n frames in nanoseconds.
func (c *Clock) FrameDurationNanos(n int64) int64 {
	return c.FramesToNanos(n)
}

// Range is a half-open integer frame interval [Start, End).
type Range struct {
	Start int64 `json:"start-frame"`
	End   int64 `json:"end-frame"`
}

// Frames returns the interval length in frames.
func (r Range) Frames() int64 {
	if r.End <= r.Start {
		return 0
	}
	return r.End - r.Start
}

// Valid reports whether the half-open range is well formed.
func (r Range) Valid() bool { return r.Start >= 0 && r.End >= r.Start }

// Contains reports whether frame f is inside [Start, End).
func (r Range) Contains(f int64) bool { return f >= r.Start && f < r.End }

// DurationNanos is the exact integer duration of the range.
func (r Range) DurationNanos(c *Clock) int64 {
	return c.FrameDurationNanos(r.Frames())
}

// Overlaps reports whether two half-open ranges intersect.
func Overlaps(a, b Range) bool {
	return a.Start < b.End && b.Start < a.End
}

// MergeUnions merges overlapping or adjacent ranges. Input need not be sorted;
// the returned ranges are sorted and disjoint.
func MergeUnions(in []Range) []Range {
	if len(in) == 0 {
		return nil
	}
	rs := append([]Range(nil), in...)
	// insertion-free simple sort via O(n log n) helper below.
	sortRanges(rs)
	out := make([]Range, 0, len(rs))
	cur := rs[0]
	for _, r := range rs[1:] {
		if r.Start <= cur.End {
			if r.End > cur.End {
				cur.End = r.End
			}
			continue
		}
		out = append(out, cur)
		cur = r
	}
	out = append(out, cur)
	return out
}

func sortRanges(rs []Range) {
	// small local sort to avoid pulling sort into the API surface noise.
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j-1].Start > rs[j].Start; j-- {
			rs[j-1], rs[j] = rs[j], rs[j-1]
		}
	}
}
