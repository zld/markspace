package ingest

import (
	"sort"

	"avcheck/internal/media"
)

// KeyIndex maps keyframe sample times to sample offsets for seek.
type KeyIndex struct {
	Times       []media.Rat // keyframe times, strictly increasing
	Offsets     []int64     // keyframe sample indexes, each within [0, SampleCount)
	SampleCount int
	Hits        uint64
	Misses      uint64
}

// NewKeyIndex validates that offsets are ordered and inside the sample range.
func NewKeyIndex(times []media.Rat, offsets []int64, sampleCount int) (*KeyIndex, error) {
	if len(times) != len(offsets) {
		return nil, media.NewError(media.KindRange, 0, 0, -1,
			"keyframe times/offsets length %d != %d", len(times), len(offsets))
	}
	if len(times) == 0 {
		return nil, media.NewError(media.KindRange, 0, 0, -1, "empty keyframe index")
	}
	for i := range times {
		if offsets[i] < 0 || offsets[i] >= int64(sampleCount) {
			return nil, media.NewError(media.KindRange, 0, uint64(i), offsets[i],
				"keyframe offset %d outside sample count %d", offsets[i], sampleCount)
		}
		if i > 0 {
			if times[i].Cmp(times[i-1]) <= 0 {
				return nil, media.NewError(media.KindRange, 0, uint64(i), -1,
					"keyframe times not increasing at %d", i)
			}
			if offsets[i] <= offsets[i-1] {
				return nil, media.NewError(media.KindRange, 0, uint64(i), offsets[i],
					"keyframe offsets not increasing at %d", i)
			}
		}
	}
	return &KeyIndex{Times: times, Offsets: offsets, SampleCount: sampleCount}, nil
}

// Locate returns the offset of the last keyframe at or before t.
func (k *KeyIndex) Locate(t media.Rat) (int64, bool) {
	i := sort.Search(len(k.Times), func(i int) bool { return k.Times[i].Cmp(t) > 0 }) - 1
	if i < 0 {
		k.Misses++
		return 0, false
	}
	k.Hits++
	return k.Offsets[i], true
}

// HitRate returns hits/(hits+misses); 1 when no lookups were made.
func (k *KeyIndex) HitRate() float64 {
	total := k.Hits + k.Misses
	if total == 0 {
		return 1
	}
	return float64(k.Hits) / float64(total)
}
