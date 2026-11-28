// Package clock defines the shared integer tick timebase used across every
// subsystem. All track-local timescales are converted into ticks with
// floor division; the per-conversion alignment error is tracked so callers
// can assert the maximum boundary error.
package clock

import (
	"sort"
)

// TicksPerSecond is the master clock rate. 90 kHz divides evenly into the
// common media timescales (24/25/30 fps, 44.1k/48k audio) while remaining a
// plain integer domain.
const TicksPerSecond int64 = 90000

// AlignmentError records the largest non-divisible conversion seen on a
// timeline. Error ticks are in the target tick domain.
type AlignmentError struct {
	MaxErrorTicks int64 `json:"max_error_ticks"`
	// Remainder/denominator of the worst conversion, retained for reports.
	WorstRemainder int64 `json:"worst_remainder"`
	WorstDenom     int64 `json:"worst_denominator"`
}

// Converter maps one source timescale onto the master tick domain.
type Converter struct {
	timescale int64
	err       AlignmentError
}

// NewConverter builds a converter for a track-local timescale in units/sec.
func NewConverter(timescale uint32) (*Converter, error) {
	if timescale == 0 {
		return nil, errZeroTimescale()
	}
	return &Converter{timescale: int64(timescale)}, nil
}

// ToTicks converts a source time to master ticks, rounding down. The dropped
// remainder updates the running maximum alignment error.
func (c *Converter) ToTicks(srcTime int64) int64 {
	num := srcTime * TicksPerSecond
	ticks := num / c.timescale
	rem := num - ticks*c.timescale // floor remainder, always >= 0
	if rem > c.err.WorstRemainder {
		c.err.WorstRemainder = rem
		c.err.WorstDenom = c.timescale
		// error expressed in target tick units, always < 1 tick.
		c.err.MaxErrorTicks = 1
	}
	return ticks
}

// AlignmentError returns the largest alignment error observed so far.
func (c *Converter) AlignmentError() AlignmentError { return c.err }

// Timescale returns the source timescale this converter wraps.
func (c *Converter) Timescale() int64 { return c.timescale }

// TimelinePoint is one integer-tick boundary on the composed timeline.
type TimelinePoint struct {
	Tick        int64
	TrackIndex  int
	SampleIndex int
}

// SortPoints orders points by tick, then track number, then sample index so
// boundaries that coincide keep a stable, reproducible ordering.
func SortPoints(p []TimelinePoint) {
	sort.SliceStable(p, func(i, j int) bool {
		if p[i].Tick != p[j].Tick {
			return p[i].Tick < p[j].Tick
		}
		if p[i].TrackIndex != p[j].TrackIndex {
			return p[i].TrackIndex < p[j].TrackIndex
		}
		return p[i].SampleIndex < p[j].SampleIndex
	})
}

// MergeMax returns the largest of several alignment errors, for combining
// per-track statistics into one timeline summary.
func MergeMax(errs ...AlignmentError) AlignmentError {
	out := AlignmentError{}
	for _, e := range errs {
		if e.WorstRemainder > out.WorstRemainder {
			out = e
		}
	}
	return out
}
