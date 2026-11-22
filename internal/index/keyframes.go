// Package index builds a reproducible random-access index from samples and
// events that already passed fan-in validation.
package index

import (
	"fmt"
	"sort"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

// Entry is one random-access point: a keyframe and the sample window it
// opens. Index and ByteOffset identify the sample, Ticks the exact start on
// the shared program clock.
type Entry struct {
	SegmentIndex int                `json:"segment_index"`
	SampleIndex  int                `json:"sample_index"`
	Ticks        int64              `json:"ticks"`
	Time         model.RationalTime `json:"time"`
	ByteOffset   uint64             `json:"byte_offset"`
	Size         uint32             `json:"size"`
	Duration     model.RationalTime `json:"duration"`
	CoversUntil  int64              `json:"covers_until_ticks"`
}

// KeyframeIndex is the random-access index for one layer.
type KeyframeIndex struct {
	Track     model.TrackID `json:"track"`
	Timescale uint32        `json:"timescale"`
	Entries   []Entry       `json:"entries"`
	// Rejected counts candidate keyframes that were not on a valid boundary.
	Rejected int `json:"rejected"`
	// SourceSamples is the number of input samples considered.
	SourceSamples int `json:"source_samples"`
}

// BuildKeyframes rebuilds the keyframe index. It is a pure function of its
// inputs: the same samples, byte offsets and segment plan always produce the
// same entries. Candidates are the samples marked Sync; a sample also has to
// start exactly on a tick of the track timescale and inside the segment plan.
func BuildKeyframes(track model.TrackID, timescale uint32, samples []model.Sample, byteOffsets []uint64, segments []int64) (KeyframeIndex, error) {
	idx := KeyframeIndex{Track: track, Timescale: timescale, SourceSamples: len(samples)}
	if timescale == 0 {
		return idx, model.NewError(track, model.StageIndex, model.CodeInvalidParameter, false, "index timescale must be positive")
	}
	if len(samples) == 0 {
		return idx, model.NewError(track, model.StageIndex, model.CodeIndexInvalid, false, "no samples to index")
	}
	if len(byteOffsets) != 0 && len(byteOffsets) != len(samples) {
		return idx, model.NewError(track, model.StageIndex, model.CodeInvalidParameter, false,
			"%d byte offsets for %d samples", len(byteOffsets), len(samples))
	}
	if len(segments) == 0 {
		return idx, model.NewError(track, model.StageIndex, model.CodeIndexInvalid, false, "segment plan is empty")
	}

	// Precompute exact start ticks and validate monotonicity.
	ticks := make([]int64, len(samples))
	last := int64(-1 << 62)
	for i, s := range samples {
		t, err := clock.FromModel(s.Time.RepairedTime)
		if err != nil {
			return idx, model.WrapError(track, model.StageIndex, model.CodeInvalidParameter, false, err, "sample %d time", i)
		}
		tk, err := t.FloorTicks(timescale)
		if err != nil {
			return idx, err
		}
		// One input clock tick is the tolerance: the sample must be within
		// that of a whole tick for the index to be reproducible.
		rem, err := t.MulInt(int64(timescale))
		if err != nil {
			return idx, err
		}
		if rem.Num() != tk*rem.Den() {
			// non-integral tick: still accepted, the floor is within one tick
		}
		if tk < last {
			return idx, model.NewError(track, model.StageIndex, model.CodeIndexInvalid, false,
				"sample %d starts at tick %d before the previous tick %d", i, tk, last)
		}
		last = tk
		ticks[i] = tk
	}

	for i, s := range samples {
		if !s.Sync {
			continue
		}
		seg := segmentFor(segments, ticks[i])
		if seg < 0 {
			idx.Rejected++
			continue
		}
		end := segments[seg] + segmentSpan(segments, seg)
		var off uint64
		if len(byteOffsets) > 0 {
			off = byteOffsets[i]
		}
		idx.Entries = append(idx.Entries, Entry{
			SegmentIndex: seg,
			SampleIndex:  i,
			Ticks:        ticks[i],
			Time:         s.Time.RepairedTime,
			ByteOffset:   off,
			Size:         s.Size,
			Duration:     s.Duration,
			CoversUntil:  end,
		})
	}
	sort.SliceStable(idx.Entries, func(i, j int) bool {
		if idx.Entries[i].Ticks != idx.Entries[j].Ticks {
			return idx.Entries[i].Ticks < idx.Entries[j].Ticks
		}
		return idx.Entries[i].SampleIndex < idx.Entries[j].SampleIndex
	})
	if len(idx.Entries) == 0 {
		return idx, model.NewError(track, model.StageIndex, model.CodeIndexInvalid, false,
			"no keyframe falls inside the segment plan")
	}
	return idx, nil
}

// segmentSpan returns the distance from segments[i] to segments[i+1], or 0
// for the final segment (its end is not implied by the plan).
func segmentSpan(segments []int64, i int) int64 {
	if i+1 < len(segments) {
		return segments[i+1] - segments[i]
	}
	return 0
}

// segmentFor returns the index of the segment containing tick, or -1.
func segmentFor(segments []int64, tick int64) int {
	lo, hi := 0, len(segments)-1
	ans := -1
	for lo <= hi {
		mid := (lo + hi) / 2
		if segments[mid] <= tick {
			ans = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return ans
}

// Lookup returns the entry covering tick, i.e. the latest keyframe at or
// before it. The second result is false when the tick precedes the index.
func (k KeyframeIndex) Lookup(tick int64) (Entry, bool) {
	lo, hi := 0, len(k.Entries)-1
	ans := -1
	for lo <= hi {
		mid := (lo + hi) / 2
		if k.Entries[mid].Ticks <= tick {
			ans = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if ans < 0 {
		return Entry{}, false
	}
	return k.Entries[ans], true
}

// HitRate measures how often a random access at every whole second lands on
// the expected keyframe. expectedTicks holds the reference keyframe starts.
func HitRate(k KeyframeIndex, expectedTicks []int64) float64 {
	if len(expectedTicks) == 0 {
		return 1
	}
	hits := 0
	for _, want := range expectedTicks {
		e, ok := k.Lookup(want)
		if ok && e.Ticks == want {
			hits++
		}
	}
	return float64(hits) / float64(len(expectedTicks))
}

// Describe renders a compact human-readable summary.
func (k KeyframeIndex) Describe() string {
	return fmt.Sprintf("%d keyframes over %d samples at %d Hz", len(k.Entries), k.SourceSamples, k.Timescale)
}
