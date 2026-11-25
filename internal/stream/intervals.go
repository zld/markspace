package stream

import (
	"sort"

	"assetcheck/internal/mediaclock"
)

// IntervalStats summarizes keyframe/boundary spacing in integer frames.
type IntervalStats struct {
	BoundaryCount int     `json:"boundary-count"`
	IntervalCount int     `json:"interval-count"`
	MinFrames     int64   `json:"min-frames"`
	MaxFrames     int64   `json:"max-frames"`
	SumFrames     int64   `json:"sum-frames"`
	AvgFrames     int64   `json:"avg-frames"`
	AvgRemainder  int64   `json:"avg-remainder"`
	Intervals     []int64 `json:"intervals,omitempty"`
}

// ComputeIntervals derives min/max/average spacing between sorted frame
// boundaries. Duplicate boundaries yield a zero interval only when
// keepDuplicates is true; the scanner normally removes duplicates before
// calling this and reports them separately.
func ComputeIntervals(boundaries []int64) IntervalStats {
	st := IntervalStats{BoundaryCount: len(boundaries)}
	if len(boundaries) < 2 {
		return st
	}
	bs := append([]int64(nil), boundaries...)
	sort.Slice(bs, func(i, j int) bool { return bs[i] < bs[j] })
	st.MinFrames = -1
	for i := 1; i < len(bs); i++ {
		d := bs[i] - bs[i-1]
		st.Intervals = append(st.Intervals, d)
		st.SumFrames += d
		if st.MinFrames == -1 || d < st.MinFrames {
			st.MinFrames = d
		}
		if d > st.MaxFrames {
			st.MaxFrames = d
		}
	}
	st.IntervalCount = len(st.Intervals)
	if st.IntervalCount > 0 {
		st.AvgFrames = st.SumFrames / int64(st.IntervalCount)
		st.AvgRemainder = st.SumFrames % int64(st.IntervalCount)
	}
	return st
}

// CorruptFrames sums the lengths of merged disjoint ranges.
func CorruptFrames(ranges []mediaclock.Range) int64 {
	var n int64
	for _, r := range mediaclock.MergeUnions(ranges) {
		n += r.Frames()
	}
	return n
}

// CorruptRatio returns corruptFrames / totalFrames as a float64. The frame
// counts themselves stay exact integers; floating point is used only for the
// final business ratio.
func CorruptRatio(corruptFrames, totalFrames int64) float64 {
	if totalFrames <= 0 {
		return 0
	}
	return float64(corruptFrames) / float64(totalFrames)
}

// FrameCeilDiv is integer ceil(a/b).
func FrameCeilDiv(a, b int64) int64 {
	if b <= 0 || a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}
