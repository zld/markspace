// Package model defines the shared nanosecond time base, sample records,
// intervals and timeline invariants used across all mediamerge modules.
package model

import (
	"fmt"
	"math"
)

// NsPerSecond is the number of nanoseconds in one second.
const NsPerSecond int64 = 1_000_000_000

// NsTime is an instant on the unified nanosecond timeline.
type NsTime int64

// NsDuration is a length of time in nanoseconds.
type NsDuration int64

// Ms converts nanoseconds to whole milliseconds.
func (d NsDuration) Ms() int64 { return int64(d) / 1_000_000 }

// Interval is a half-open range [Start, End) on the nanosecond timeline.
type Interval struct {
	Start NsTime `json:"start_ns"`
	End   NsTime `json:"end_ns"`
}

// Contains reports whether t falls inside the interval.
func (iv Interval) Contains(t NsTime) bool { return t >= iv.Start && t < iv.End }

// Len returns the interval duration.
func (iv Interval) Len() NsDuration { return NsDuration(iv.End - iv.Start) }

// Timescale converts between media ticks and nanoseconds using exact
// rational arithmetic (num ticks = den nanoseconds is avoided; instead a
// timescale of N means N ticks per second).
type Timescale struct {
	TicksPerSecond uint64 `json:"ticks_per_second"`
}

// ToNs converts a tick count to nanoseconds, rounding half away from zero.
// The signed rounding error in nanoseconds (actual - exact) is returned so
// callers can account for alignment error deterministically. Values that
// would overflow the nanosecond range saturate at the representable
// maximum rather than wrapping.
func (ts Timescale) ToNs(ticks uint64) (NsTime, float64) {
	if ts.TicksPerSecond == 0 {
		return 0, 0
	}
	const maxTicks = uint64(math.MaxInt64) / uint64(NsPerSecond)
	if ticks > maxTicks {
		return NsTime(math.MaxInt64), 0
	}
	num := ticks * uint64(NsPerSecond)
	whole := num / ts.TicksPerSecond
	rem := num % ts.TicksPerSecond
	if rem >= (ts.TicksPerSecond+1)/2 {
		whole++
	}
	if whole > uint64(math.MaxInt64) {
		return NsTime(math.MaxInt64), 0
	}
	errNs := (float64(whole)*float64(ts.TicksPerSecond) - float64(num)) / float64(ts.TicksPerSecond)
	return NsTime(whole), errNs
}

// Sample is one media sample located on the unified timeline, retaining its
// origin (source file, byte offset, fragment sequence) for traceability.
type Sample struct {
	TrackID    uint32     `json:"track_id"`
	SeqNum     uint32     `json:"seq_num"` // mfhd sequence number of the source fragment
	SourceFile string     `json:"source_file"`
	ByteOffset int64      `json:"byte_offset"` // absolute offset of sample payload in SourceFile
	Size       uint32     `json:"size"`
	Start      NsTime     `json:"start_ns"`
	Dur        NsDuration `json:"dur_ns"`
	Index      uint32     `json:"index"` // sample index inside its fragment
}

// End returns the exclusive end time of the sample.
func (s Sample) End() NsTime { return s.Start + NsTime(s.Dur) }

// Key orders samples deterministically: track, then start, then origin.
func (s Sample) Key() string {
	return fmt.Sprintf("%08x/%016x/%s/%08x/%04x", s.TrackID, uint64(s.Start), s.SourceFile, s.SeqNum, s.Index)
}

// Gap describes a missing mfhd sequence range on one track. No samples are
// fabricated for a gap; it is surfaced in the manifest instead.
type Gap struct {
	TrackID    uint32   `json:"track_id"`
	AfterSeq   uint32   `json:"after_seq"`
	BeforeSeq  uint32   `json:"before_seq"`
	Time       Interval `json:"time"`
	EstSamples int      `json:"est_missing_samples"`
}

// CheckMonotonic verifies the per-track timeline invariant: within one
// track, sample start times must be non-decreasing. It returns the first
// offending sample index, or -1 when the invariant holds.
func CheckMonotonic(samples []Sample) int {
	last := make(map[uint32]NsTime)
	seen := make(map[uint32]bool)
	for i, s := range samples {
		if seen[s.TrackID] && s.Start < last[s.TrackID] {
			return i
		}
		seen[s.TrackID] = true
		last[s.TrackID] = s.Start
	}
	return -1
}
