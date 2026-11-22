// Package index builds a keyframe index over variable-frame-rate media.
//
// Samples carry no fixed frame duration, so the index stores cumulative start
// times derived from each sample's own duration. Lookups return the latest
// keyframe at or before a target time, which is what a segmenter needs to cut
// without re-encoding, and report out-of-range and empty-index conditions
// explicitly.
package index

import (
	"sort"

	"example.com/mediaforge/internal/merrors"
)

// Entry is one keyframe's position.
type Entry struct {
	// SampleIndex is the 0-based index of the keyframe sample in the track.
	SampleIndex int
	// Tick is the keyframe's start time in track timescale units.
	Tick uint64
}

// Index is an immutable keyframe lookup table.
type Index struct {
	timescale uint32
	entries   []Entry
	// endTick is the end of the last sample, used for range checks.
	endTick uint64
	samples int
}

// MaxSamples bounds the input to Build.
const MaxSamples = 1_000_000

// Build constructs an index from per-sample durations and keyframe flags.
// durations and keyframes must have the same length; sample i starts at the
// sum of all preceding durations.
func Build(timescale uint32, durations []uint64, keyframes []bool) (*Index, error) {
	if timescale == 0 {
		return nil, merrors.New(merrors.StageIndex, merrors.Invalid, "timescale must be positive")
	}
	if len(durations) != len(keyframes) {
		return nil, merrors.New(merrors.StageIndex, merrors.Invalid,
			"%d durations but %d keyframe flags", len(durations), len(keyframes))
	}
	if len(durations) > MaxSamples {
		return nil, merrors.New(merrors.StageIndex, merrors.Bounds,
			"%d samples exceeds limit %d", len(durations), MaxSamples)
	}
	if len(durations) == 0 {
		return &Index{timescale: timescale}, nil
	}

	idx := &Index{timescale: timescale, samples: len(durations)}
	var tick uint64
	for i, d := range durations {
		if d == 0 {
			return nil, merrors.At(merrors.StageIndex, merrors.Invalid, int64(i),
				"sample %d has zero duration", i)
		}
		if tick+d < tick {
			return nil, merrors.At(merrors.StageIndex, merrors.Overflow, int64(i),
				"cumulative time overflows at sample %d", i)
		}
		if keyframes[i] {
			idx.entries = append(idx.entries, Entry{SampleIndex: i, Tick: tick})
		}
		tick += d
	}
	idx.endTick = tick
	return idx, nil
}

// Empty reports whether the index has no samples at all.
func (x *Index) Empty() bool { return x.samples == 0 }

// HasKeyframes reports whether any sample is a keyframe.
func (x *Index) HasKeyframes() bool { return len(x.entries) > 0 }

// Samples returns the number of samples indexed.
func (x *Index) Samples() int { return x.samples }

// EndTick returns the end of the last sample, i.e. the media duration.
func (x *Index) EndTick() uint64 { return x.endTick }

// Timescale returns the units per second.
func (x *Index) Timescale() uint32 { return x.timescale }

// Entries returns a copy of the keyframe table.
func (x *Index) Entries() []Entry {
	out := make([]Entry, len(x.entries))
	copy(out, x.entries)
	return out
}

// Lookup returns the latest keyframe at or before target tick.
//
// A target before the first keyframe yields the first keyframe, flagged as
// clamped, because there is no earlier decodable point. A target at or past
// the end of the media is out of range. An index with samples but no
// keyframes is reported as empty.
func (x *Index) Lookup(target uint64) (Entry, error) {
	if x.samples == 0 {
		return Entry{}, merrors.New(merrors.StageIndex, merrors.Empty, "index has no samples")
	}
	if !x.HasKeyframes() {
		return Entry{}, merrors.New(merrors.StageIndex, merrors.Empty,
			"index has %d samples but no keyframes", x.samples)
	}
	if target >= x.endTick {
		return Entry{}, merrors.At(merrors.StageIndex, merrors.OutOfRange, int64(target),
			"target %d at or past end %d", target, x.endTick)
	}
	if target < x.entries[0].Tick {
		return x.entries[0], merrors.At(merrors.StageIndex, merrors.OutOfRange, int64(target),
			"target %d precedes first keyframe %d; clamping", target, x.entries[0].Tick)
	}
	// First entry strictly after target, then step back.
	i := sort.Search(len(x.entries), func(k int) bool { return x.entries[k].Tick > target })
	return x.entries[i-1], nil
}

// NearestKeyframe is a lookup that tolerates clamping: it returns the entry
// alongside the error so callers can use the clamped result if they choose.
func (x *Index) NearestKeyframe(target uint64) (Entry, error) { return x.Lookup(target) }

// KeyframesInRange returns the keyframes whose tick lies in [from, to).
func (x *Index) KeyframesInRange(from, to uint64) []Entry {
	if from >= to {
		return nil
	}
	lo := sort.Search(len(x.entries), func(k int) bool { return x.entries[k].Tick >= from })
	hi := sort.Search(len(x.entries), func(k int) bool { return x.entries[k].Tick >= to })
	if lo >= hi {
		return nil
	}
	out := make([]Entry, hi-lo)
	copy(out, x.entries[lo:hi])
	return out
}

// CutPoints splits the media into count equal-duration segments and returns
// the keyframe at or before each segment boundary, deduplicated and in order.
// The first segment always starts at the first keyframe. This is how the
// pipeline derives deterministic segment boundaries from VFR media.
func (x *Index) CutPoints(count int) ([]Entry, error) {
	if count <= 0 {
		return nil, merrors.New(merrors.StageIndex, merrors.Invalid, "segment count %d", count)
	}
	if x.samples == 0 {
		return nil, merrors.New(merrors.StageIndex, merrors.Empty, "index has no samples")
	}
	if !x.HasKeyframes() {
		return nil, merrors.New(merrors.StageIndex, merrors.Empty, "no keyframes")
	}
	if count > len(x.entries) {
		return nil, merrors.New(merrors.StageIndex, merrors.OutOfRange,
			"%d segments requested but only %d keyframes", count, len(x.entries))
	}

	out := make([]Entry, 0, count)
	last := -1
	for i := 0; i < count; i++ {
		// Segment boundaries at i*duration/count, computed with integer math
		// to stay exact for large timelines.
		boundary := x.endTick / uint64(count) * uint64(i)
		rem := x.endTick % uint64(count)
		boundary += rem * uint64(i) / uint64(count)
		e, err := x.Lookup(boundary)
		if err != nil {
			// A boundary at the very end clamps to the last keyframe.
			if merrors.IsCategory(err, merrors.OutOfRange) {
				e = x.entries[len(x.entries)-1]
			} else {
				return nil, err
			}
		}
		if e.SampleIndex == last {
			continue
		}
		last = e.SampleIndex
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, merrors.New(merrors.StageIndex, merrors.Empty, "no cut points")
	}
	return out, nil
}
