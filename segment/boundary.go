package segment

import (
	"fmt"
	"sort"

	"mediapack/model"
)

// Boundary is one actual segmentation cut on the common nanosecond timeline.
type Boundary struct {
	Index        int   `json:"index"`
	DesiredNanos int64 `json:"desired_nanos"`
	Nanos        int64 `json:"nanos"`
	Delayed      bool  `json:"delayed"`
	DelayNanos   int64 `json:"delay_nanos"`
	Keyframe     bool  `json:"keyframe"`
}

// Interval is a half-open [StartNanos, EndNanos) segment span.
type Interval struct {
	Index      int   `json:"index"`
	StartNanos int64 `json:"start_nanos"`
	EndNanos   int64 `json:"end_nanos"`
}

// PlanConfig controls common-timeline boundary selection.
type PlanConfig struct {
	SegmentDurationNanos int64
	DurationNanos        int64
	Keyframes            []int64
	Track                model.TrackID
}

// PlanBoundaries chooses cuts at regular target intervals, delaying any target
// without a keyframe to the next available keyframe. The resulting plan is
// representation-independent and must be applied identically to every bitrate.
func PlanBoundaries(cfg PlanConfig) ([]Boundary, []Interval, error) {
	if cfg.SegmentDurationNanos <= 0 {
		return nil, nil, model.NewError(cfg.Track, "", -1, model.CategoryInvalidArgument, "segment duration must be positive", nil)
	}
	if cfg.DurationNanos <= 0 || cfg.SegmentDurationNanos > cfg.DurationNanos {
		return nil, nil, model.NewError(cfg.Track, "", -1, model.CategoryInvalidArgument, "segment duration must fit positive media duration", nil)
	}
	kfs := append([]int64(nil), cfg.Keyframes...)
	sort.Slice(kfs, func(i, j int) bool { return kfs[i] < kfs[j] })
	if len(kfs) == 0 || kfs[0] != 0 {
		return nil, nil, model.NewError(cfg.Track, "", -1, model.CategoryMissingKeyframe, "keyframe at timeline zero is required", nil)
	}

	boundaries := []Boundary{{Index: 0, DesiredNanos: 0, Nanos: 0, Keyframe: true}}
	last := int64(0)
	index := 1
	for desired := cfg.SegmentDurationNanos; desired < cfg.DurationNanos; desired += cfg.SegmentDurationNanos {
		cut, ok := nextKeyframeAtOrAfter(kfs, desired)
		if !ok || cut >= cfg.DurationNanos {
			// Do not seal a boundary that cannot be backed by a keyframe before EOF.
			continue
		}
		if cut <= last {
			next, ok := nextKeyframeAfter(kfs, last)
			if !ok || next >= cfg.DurationNanos {
				continue
			}
			cut = next
		}
		b := Boundary{
			Index:        index,
			DesiredNanos: desired,
			Nanos:        cut,
			Delayed:      cut > desired,
			DelayNanos:   cut - desired,
			Keyframe:     true,
		}
		boundaries = append(boundaries, b)
		last = cut
		index++
	}

	intervals := make([]Interval, 0, len(boundaries))
	for i, b := range boundaries {
		end := cfg.DurationNanos
		if i+1 < len(boundaries) {
			end = boundaries[i+1].Nanos
		}
		if end <= b.Nanos {
			return nil, nil, model.NewError(cfg.Track, "", b.Nanos, model.CategoryInvalidArgument,
				fmt.Sprintf("non-positive interval at boundary %d", b.Index), nil)
		}
		intervals = append(intervals, Interval{Index: i, StartNanos: b.Nanos, EndNanos: end})
	}
	return boundaries, intervals, nil
}

func nextKeyframeAtOrAfter(kfs []int64, target int64) (int64, bool) {
	i := sort.Search(len(kfs), func(i int) bool { return kfs[i] >= target })
	if i == len(kfs) {
		return 0, false
	}
	return kfs[i], true
}

func nextKeyframeAfter(kfs []int64, after int64) (int64, bool) {
	i := sort.Search(len(kfs), func(i int) bool { return kfs[i] > after })
	if i == len(kfs) {
		return 0, false
	}
	return kfs[i], true
}

// BoundaryNanos returns just the actual cut times, useful for equality checks.
func BoundaryNanos(boundaries []Boundary) []int64 {
	out := make([]int64, 0, len(boundaries))
	for _, b := range boundaries {
		out = append(out, b.Nanos)
	}
	return out
}

// EqualBoundaries reports whether two representation plans have identical cuts.
func EqualBoundaries(a, b []Boundary) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Nanos != b[i].Nanos || a[i].Index != b[i].Index {
			return false
		}
	}
	return true
}
