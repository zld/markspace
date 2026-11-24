// Package clock defines the shared nanosecond media timeline used by scan,
// repair, indexing, verification and reporting. Integer arithmetic keeps
// results identical on arm64 and amd64.
package clock

import (
	"fmt"
	"math"
	"math/bits"
)

// NsPerSecond is the shared unit. Every reported time is integer nanoseconds.
const NsPerSecond int64 = 1_000_000_000

// Base is the media time base for one track. A tick value is converted with a
// single rounded integer division from the absolute value, never by summing
// per-sample deltas, so cumulative error stays at most half a nanosecond for
// every timestamp even on multi-hour recordings.
type Base struct {
	Timescale uint32 `json:"timescale"`
	OriginNs  int64  `json:"origin_ns"`
}

// NewBase validates a timescale and builds a base whose tick zero maps to
// originNs. OriginNs carries a preserved start offset into the shared timeline.
func NewBase(timescale uint32, originNs int64) (Base, error) {
	if timescale == 0 || timescale > 1_000_000_000 {
		return Base{}, New(StageScan, CodeBadTimescale, "timescale %d out of range", timescale)
	}
	return Base{Timescale: timescale, OriginNs: originNs}, nil
}

// TicksToNs converts ticks to nanoseconds using exact 128-bit arithmetic and
// round-half-away-from-zero. Negative media times are preserved.
func TicksToNs(timescale uint32, ticks int64) int64 {
	if timescale == 0 {
		return 0
	}
	neg := ticks < 0
	mag := uint64(ticks)
	if neg {
		mag = uint64(-ticks)
	}
	hi, lo := bits.Mul64(mag, uint64(NsPerSecond))
	q, r := bits.Div64(hi, lo, uint64(timescale))
	if r*2 >= uint64(timescale) {
		q++
	}
	if q > math.MaxInt64 {
		if neg {
			return math.MinInt64
		}
		return math.MaxInt64
	}
	if neg {
		return -int64(q)
	}
	return int64(q)
}

// NsToTicks converts nanoseconds to media ticks, rounded to nearest.
func NsToTicks(timescale uint32, ns int64) int64 {
	if timescale == 0 {
		return 0
	}
	neg := ns < 0
	mag := uint64(ns)
	if neg {
		mag = uint64(-ns)
	}
	hi, lo := bits.Mul64(mag, uint64(timescale))
	q, r := bits.Div64(hi, lo, uint64(NsPerSecond))
	if r*2 >= uint64(NsPerSecond) {
		q++
	}
	if q > math.MaxInt64 {
		if neg {
			return math.MinInt64
		}
		return math.MaxInt64
	}
	if neg {
		return -int64(q)
	}
	return int64(q)
}

// ToNs maps absolute media ticks into the shared timeline.
func (b Base) ToNs(ticks int64) int64 { return b.OriginNs + TicksToNs(b.Timescale, ticks) }

// DurationNs converts a non-negative tick duration.
func (b Base) DurationNs(ticks uint64) int64 {
	if ticks > math.MaxInt64 {
		return math.MaxInt64
	}
	return TicksToNs(b.Timescale, int64(ticks))
}

// FromNs maps a shared-timeline timestamp back to track ticks.
func (b Base) FromNs(ns int64) int64 { return NsToTicks(b.Timescale, ns-b.OriginNs) }

// EditEntry mirrors an elst entry while keeping both timescales needed to
// interpret it. SegmentDuration is in the movie timescale, MediaTime in the
// media timescale. MediaTime < 0 denotes an empty edit (a gap).
type EditEntry struct {
	SegmentDuration uint64 `json:"segment_duration"`
	MediaTime       int64  `json:"media_time"`
	Rate            int32  `json:"media_rate"`
	RawOffset       int64  `json:"raw_offset"`
}

// Empty reports whether the entry is an empty edit.
func (e EditEntry) Empty() bool { return e.MediaTime < 0 }

// EditList preserves the original edit list mapping exactly as stored.
type EditList struct {
	MediaTimescale uint32      `json:"media_timescale"`
	MovieTimescale uint32      `json:"movie_timescale"`
	Entries        []EditEntry `json:"entries,omitempty"`
}

// FirstMediaTime returns the first non-empty media time, i.e. the preserved
// source start offset.
func (l EditList) FirstMediaTime() int64 {
	for _, e := range l.Entries {
		if !e.Empty() {
			return e.MediaTime
		}
	}
	return 0
}

// LeadNs is the presentation delay created by leading empty edits.
func (l EditList) LeadNs() int64 {
	var ns int64
	for _, e := range l.Entries {
		if !e.Empty() {
			break
		}
		ns += TicksToNs(orOne(l.MovieTimescale), int64(minU64(e.SegmentDuration, math.MaxInt64)))
	}
	return ns
}

// TotalNs is the presentation duration described by the edit list.
func (l EditList) TotalNs() int64 {
	if len(l.Entries) == 0 {
		return 0
	}
	var total uint64
	for _, e := range l.Entries {
		total += e.SegmentDuration
	}
	return TicksToNs(orOne(l.MovieTimescale), int64(minU64(total, math.MaxInt64)))
}

// PresentationNs maps a media tick to the presentation timeline. The bool is
// false when the edit list does not cover the tick. With no edit list the
// media timeline is used directly.
func (l EditList) PresentationNs(mediaTicks int64) (int64, bool) {
	if len(l.Entries) == 0 {
		return TicksToNs(orOne(l.MediaTimescale), mediaTicks), true
	}
	var moviePos uint64
	for _, e := range l.Entries {
		if e.Empty() {
			moviePos += e.SegmentDuration
			continue
		}
		durMedia := segmentMediaTicks(l, e)
		if mediaTicks >= e.MediaTime && mediaTicks < e.MediaTime+durMedia {
			return TicksToNs(orOne(l.MovieTimescale), int64(minU64(moviePos, math.MaxInt64))) +
				TicksToNs(orOne(l.MediaTimescale), mediaTicks-e.MediaTime), true
		}
		moviePos += e.SegmentDuration
	}
	return 0, false
}

// MediaTicksAtNs inverts PresentationNs for a presentation timestamp.
func (l EditList) MediaTicksAtNs(ns int64) (int64, bool) {
	if len(l.Entries) == 0 {
		return NsToTicks(orOne(l.MediaTimescale), ns), true
	}
	var moviePos int64
	for _, e := range l.Entries {
		durNs := TicksToNs(orOne(l.MovieTimescale), int64(minU64(e.SegmentDuration, math.MaxInt64)))
		if e.Empty() {
			moviePos += durNs
			continue
		}
		if ns < moviePos+durNs {
			return e.MediaTime + NsToTicks(orOne(l.MediaTimescale), ns-moviePos), true
		}
		moviePos += durNs
	}
	return 0, false
}

// MediaDurationTicks approximates the media span covered by the edit list.
func (l EditList) MediaDurationTicks() int64 {
	if len(l.Entries) == 0 {
		return 0
	}
	var total int64
	for _, e := range l.Entries {
		if !e.Empty() {
			total += segmentMediaTicks(l, e)
		}
	}
	return total
}

func segmentMediaTicks(l EditList, e EditEntry) int64 {
	durNs := TicksToNs(orOne(l.MovieTimescale), int64(minU64(e.SegmentDuration, math.MaxInt64)))
	return NsToTicks(orOne(l.MediaTimescale), durNs)
}

// AlignmentErrorNs compares two intervals and returns the larger endpoint
// difference.
func AlignmentErrorNs(wantStart, wantEnd, gotStart, gotEnd int64) int64 {
	d := abs64(wantStart - gotStart)
	if e := abs64(wantEnd - gotEnd); e > d {
		d = e
	}
	return d
}

func abs64(v int64) int64 {
	if v < 0 {
		if v == math.MinInt64 {
			return math.MaxInt64
		}
		return -v
	}
	return v
}

func orOne(v uint32) uint32 {
	if v == 0 {
		return 1
	}
	return v
}

func minU64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// FormatNs renders nanoseconds with microsecond resolution for logs.
func FormatNs(ns int64) string {
	neg := ""
	if ns < 0 {
		neg = "-"
		ns = -ns
	}
	return fmt.Sprintf("%s%d.%06ds", neg, ns/NsPerSecond, (ns%NsPerSecond)/1000)
}
