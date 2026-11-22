package index

import (
	"sort"

	"mediacore"
	"mediacore/syncer"
)

// KeyframeIndex maps a parsed track's keyframes onto the shared integer time
// base and supports deterministic nearest-keyframe lookups.
type KeyframeIndex struct {
	track mediacore.TrackID
	// frames holds (sample index, presentation ns) pairs for keyframes only.
	frames []keyPoint
}

type keyPoint struct {
	Index int
	Time  int64
}

// BuildKeyframeIndex constructs the index from a parsed track's samples.
func BuildKeyframeIndex(ti TrackInfo) *KeyframeIndex {
	k := &KeyframeIndex{track: ti.Track}
	for _, s := range ti.Samples {
		if s.Keyframe {
			k.frames = append(k.frames, keyPoint{Index: s.Index, Time: s.Presentation})
		}
	}
	sort.Slice(k.frames, func(i, j int) bool { return k.frames[i].Time < k.frames[j].Time })
	return k
}

// Count returns the number of keyframes.
func (k *KeyframeIndex) Count() int { return len(k.frames) }

// Frames returns the keyframe sample indexes in presentation order.
func (k *KeyframeIndex) Frames() []int {
	out := make([]int, len(k.frames))
	for i, p := range k.frames {
		out[i] = p.Index
	}
	return out
}

// NearestAt returns the keyframe at or before timeNS (random-access point),
// or ok=false when timeNS precedes the first keyframe.
func (k *KeyframeIndex) NearestAt(timeNS int64) (idx int, time int64, ok bool) {
	if len(k.frames) == 0 || timeNS < k.frames[0].Time {
		return 0, 0, false
	}
	i := sort.Search(len(k.frames), func(i int) bool { return k.frames[i].Time > timeNS }) - 1
	return k.frames[i].Index, k.frames[i].Time, true
}

// HitRate reports the fraction of requested seek times whose nearest
// keyframe lands exactly on a true keyframe sample. It is deterministic for a
// given request set.
func (k *KeyframeIndex) HitRate(requests []int64) float64 {
	if len(requests) == 0 {
		return 0
	}
	keyed := make(map[int]bool)
	for _, p := range k.frames {
		keyed[p.Index] = true
	}
	hits := 0
	for _, at := range requests {
		idx, _, ok := k.NearestAt(at)
		if !ok {
			continue
		}
		// A hit means the chosen random-access sample is genuinely keyframe.
		if keyed[idx] {
			hits++
		}
	}
	return float64(hits) / float64(len(requests))
}

// ToFrameSamples adapts parsed samples for the VFR mapper, applying the
// track's start offset onto the shared timeline.
func ToFrameSamples(ti TrackInfo) []syncer.FrameSample {
	out := make([]syncer.FrameSample, len(ti.Samples))
	for i, s := range ti.Samples {
		out[i] = syncer.FrameSample{
			Index:    s.Index,
			Time:     s.Presentation + ti.StartOffset,
			Keyframe: s.Keyframe,
		}
	}
	return out
}
