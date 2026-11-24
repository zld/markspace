// Package index records keyframes with both original timestamps and the
// corrected shared nanosecond timeline.
package index

import (
	"sort"

	"github.com/zld/mediarc/internal/model"
)

// Keyframe is one decodable boundary candidate.
type Keyframe struct {
	TrackID        uint32   `json:"track_id"`
	SampleNumber   uint32   `json:"sample_number"`
	RawTimestamp   uint64   `json:"raw_timestamp"`
	Timescale      uint32   `json:"timescale"`
	TimeNS         model.NS `json:"time_ns"`
	Offset         int64    `json:"byte_offset"`
	Representation string   `json:"representation"`
}

// State is the serializable index portion of a checkpoint.
type State struct {
	Keyframes []Keyframe `json:"keyframes"`
}

// Index is an ordered, track-partitioned keyframe index.
type Index struct {
	keyframes []Keyframe
	byTrack   map[uint32][]Keyframe
}

// Build creates a stable keyframe index from timed points.
func Build(points []Keyframe) *Index {
	out := &Index{byTrack: map[uint32][]Keyframe{}}
	out.keyframes = append(out.keyframes, points...)
	sort.SliceStable(out.keyframes, func(i, j int) bool {
		a, b := out.keyframes[i], out.keyframes[j]
		if a.TimeNS != b.TimeNS {
			return a.TimeNS < b.TimeNS
		}
		if a.TrackID != b.TrackID {
			return a.TrackID < b.TrackID
		}
		return a.SampleNumber < b.SampleNumber
	})
	for _, kf := range out.keyframes {
		out.byTrack[kf.TrackID] = append(out.byTrack[kf.TrackID], kf)
	}
	return out
}

// State returns a deterministic checkpoint copy.
func (idx *Index) State() State {
	if idx == nil {
		return State{}
	}
	kfs := append([]Keyframe(nil), idx.keyframes...)
	return State{Keyframes: kfs}
}

// FromState restores an index from checkpoint state.
func FromState(st State) *Index { return Build(append([]Keyframe(nil), st.Keyframes...)) }

// All returns keyframes ordered by shared time.
func (idx *Index) All() []Keyframe {
	if idx == nil {
		return nil
	}
	return append([]Keyframe(nil), idx.keyframes...)
}

// Track returns keyframes for one representation.
func (idx *Index) Track(trackID uint32) []Keyframe {
	if idx == nil {
		return nil
	}
	return append([]Keyframe(nil), idx.byTrack[trackID]...)
}

// Nearest returns the keyframe nearest target on the requested track and its
// signed deviation (keyframe time minus target).
func (idx *Index) Nearest(trackID uint32, target model.NS) (Keyframe, model.NS, bool) {
	frames := idx.Track(trackID)
	if len(frames) == 0 {
		return Keyframe{}, 0, false
	}
	pos := sort.Search(len(frames), func(i int) bool { return frames[i].TimeNS >= target })
	candidates := make([]int, 0, 2)
	if pos < len(frames) {
		candidates = append(candidates, pos)
	}
	if pos > 0 {
		candidates = append(candidates, pos-1)
	}
	best := frames[candidates[0]]
	bestDev := best.TimeNS - target
	for _, ci := range candidates[1:] {
		kf := frames[ci]
		dev := kf.TimeNS - target
		if dev.Abs() < bestDev.Abs() || (dev.Abs() == bestDev.Abs() && dev < bestDev) {
			best, bestDev = kf, dev
		}
	}
	return best, bestDev, true
}

// HitRate reports the fraction of requested boundary sample numbers that are
// present in the keyframe index.
func HitRate(selected []Keyframe) float64 {
	if len(selected) == 0 {
		return 1
	}
	hits := 0
	for _, kf := range selected {
		if kf.SampleNumber != 0 {
			hits++
		}
	}
	return float64(hits) / float64(len(selected))
}
