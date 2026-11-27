// Package clock implements the shared time model: integer nanoseconds,
// media timescale conversion, monotonic timelines and percentile helpers.
//
// All time values crossing subsystem boundaries are either integer
// nanoseconds since the media origin or explicitly labelled sample ticks
// together with their timescale. Floating point is never used to place
// samples, so timelines are bit-for-bit reproducible.
package clock

import (
	"math"
	"sort"

	merr "mediatool/internal/errors"
)

// NanosPerSecond is the resolution of every timeline in the kernel.
const NanosPerSecond int64 = 1_000_000_000

// ScaleToNanos converts ticks counted in timescale units to integer
// nanoseconds. A zero timescale is invalid.
func ScaleToNanos(timescale uint32, ticks int64) (int64, error) {
	if timescale == 0 {
		return 0, merr.New(merr.CodeInvalidBox, "zero timescale cannot be converted to nanoseconds")
	}
	if ticks == 0 {
		return 0, nil
	}
	// Split to keep the multiplication within int64 for long assets.
	secs := ticks / int64(timescale)
	rem := ticks % int64(timescale)
	if rem < 0 {
		secs--
		rem += int64(timescale)
	}
	base, overflow := mulSaturate(secs, NanosPerSecond)
	// rem < timescale <= 2^32, so rem*1e9 stays within int64.
	add, _ := mulSaturate(rem, NanosPerSecond)
	if overflow {
		return math.MaxInt64, nil
	}
	return base + add/int64(timescale), nil
}

// NanosToScale converts integer nanoseconds to ticks of the given timescale,
// rounding to the nearest tick. The split keeps the multiplication inside
// int64 even for multi-hour timestamps.
func NanosToScale(timescale uint32, nanos int64) (int64, error) {
	if timescale == 0 {
		return 0, merr.New(merr.CodeInvalidBox, "zero timescale")
	}
	ts := int64(timescale)
	sign := int64(1)
	if nanos < 0 {
		sign, nanos = -1, -nanos
	}
	secs, rem := nanos/NanosPerSecond, nanos%NanosPerSecond
	// rem < 1e9 and ts <= 2^32, so rem*ts cannot overflow int64.
	ticks := secs*ts + (rem*ts+NanosPerSecond/2)/NanosPerSecond
	return sign * ticks, nil
}

func mulSaturate(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, false
	}
	r := a * b
	if r/b != a {
		if (a < 0) != (b < 0) {
			return math.MinInt64, true
		}
		return math.MaxInt64, true
	}
	return r, false
}

// Sample is one timeline entry. PTS/DTS/Duration are integer nanoseconds.
type Sample struct {
	Index      int
	PTS        int64
	DTS        int64
	Duration   int64
	FileOffset int64
	Size       uint32
	Key        bool
}

// Timeline is a strictly append-only, monotonically non-decreasing sequence
// of samples. VFR content is positioned by its actual PTS: no constant frame
// duration is ever assumed.
type Timeline struct {
	samples []Sample
}

// NewTimeline returns an empty timeline.
func NewTimeline() *Timeline {
	return &Timeline{samples: make([]Sample, 0, 256)}
}

// Append validates and appends a sample. It rejects any PTS or DTS that would
// move the timeline backwards and requires DTS <= PTS.
func (t *Timeline) Append(s Sample) error {
	if n := len(t.samples); n > 0 {
		prev := t.samples[n-1]
		if s.PTS < prev.PTS {
			return merr.New(merr.CodeTimeWentBack,
				"sample %d PTS %d ns precedes previous PTS %d ns", s.Index, s.PTS, prev.PTS)
		}
		if s.DTS < prev.DTS {
			return merr.New(merr.CodeTimeWentBack,
				"sample %d DTS %d ns precedes previous DTS %d ns", s.Index, s.DTS, prev.DTS)
		}
	}
	if s.DTS > s.PTS {
		return merr.New(merr.CodeTimeWentBack,
			"sample %d DTS %d ns is after PTS %d ns", s.Index, s.DTS, s.PTS)
	}
	if s.Duration < 0 {
		return merr.New(merr.CodeDiscontinuity, "sample %d has negative duration", s.Index)
	}
	s.Index = len(t.samples)
	t.samples = append(t.samples, s)
	return nil
}

// Samples returns the contained samples.
func (t *Timeline) Samples() []Sample { return t.samples }

// Len implements sort-friendly access.
func (t *Timeline) Len() int { return len(t.samples) }

// Range returns the first PTS and the end timestamp of the last sample.
func (t *Timeline) Range() (start, end int64) {
	if len(t.samples) == 0 {
		return 0, 0
	}
	last := t.samples[len(t.samples)-1]
	return t.samples[0].PTS, last.PTS + last.Duration
}

// At returns the index of the sample covering the given nanosecond timestamp,
// using each sample's own [PTS, PTS+Duration) interval. This is the VFR-safe
// replacement for multiplying a frame number by a fixed frame duration.
func (t *Timeline) At(nanos int64) int {
	i := sort.Search(len(t.samples), func(i int) bool {
		s := t.samples[i]
		return nanos < s.PTS+s.Duration
	})
	if i == len(t.samples) {
		return i - 1
	}
	return i
}

// Percentile returns the p-th percentile (0 < p <= 100) of a latency/duration
// sample set using nearest-rank on a copied, sorted slice.
func Percentile(values []int64, p float64) int64 {
	if len(values) == 0 || p <= 0 {
		return 0
	}
	if p > 100 {
		p = 100
	}
	cp := append([]int64(nil), values...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	rank := int(math.Ceil(p/100*float64(len(cp)))) - 1
	if rank < 0 {
		rank = 0
	}
	return cp[rank]
}
