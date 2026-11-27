package media

import "sort"

// SampleIndex is the per-track sample and keyframe index on the unified
// nanosecond time base. Timestamps are derived from cumulative tick
// counts so frame-rate rounding never accumulates.
type SampleIndex struct {
	Track         *TrackInfo
	SampleTimes   []int64 // decode time of each sample, nanoseconds
	KeyframeTimes []int64 // decode time of each keyframe, nanoseconds
}

// BuildIndex expands the stts table into per-sample nanosecond
// timestamps. Non-increasing sample times are reported as a time
// regression against the track; they are never silently accepted.
func BuildIndex(t *TrackInfo) (*SampleIndex, error) {
	ix := &SampleIndex{Track: t}
	var units uint64
	var prev int64 = -1
	for _, e := range t.Stts {
		for i := uint32(0); i < e.Count; i++ {
			ns, err := Scale(units, t.Timescale)
			if err != nil {
				if me, ok := err.(*Error); ok {
					me.TrackID = t.TrackID
				}
				return nil, err
			}
			if prev >= 0 && ns <= prev {
				return nil, &Error{Kind: KindTimeRegression, Op: "index.build",
					TrackID: t.TrackID,
					Msg: "sample decode time is not strictly increasing"}
			}
			ix.SampleTimes = append(ix.SampleTimes, ns)
			prev = ns
			units += uint64(e.Delta)
		}
	}
	if len(ix.SampleTimes) == 0 {
		return nil, &Error{Kind: KindTruncatedBox, Op: "index.build",
			TrackID: t.TrackID, Msg: "track has no samples; refusing empty index"}
	}
	if t.KeyframeNums == nil {
		// No stss table: every sample is a keyframe (MP4 convention).
		ix.KeyframeTimes = ix.SampleTimes
		return ix, nil
	}
	for _, n := range t.KeyframeNums {
		if n == 0 || int(n) > len(ix.SampleTimes) {
			return nil, &Error{Kind: KindTruncatedBox, Op: "index.build",
				TrackID: t.TrackID,
				Msg: "stss references a sample outside the track"}
		}
		ix.KeyframeTimes = append(ix.KeyframeTimes, ix.SampleTimes[n-1])
	}
	return ix, nil
}

// HasKeyframeAt reports whether a keyframe sits exactly at the given
// nanosecond timestamp.
func (ix *SampleIndex) HasKeyframeAt(ns int64) bool {
	i := sort.Search(len(ix.KeyframeTimes), func(i int) bool {
		return ix.KeyframeTimes[i] >= ns
	})
	return i < len(ix.KeyframeTimes) && ix.KeyframeTimes[i] == ns
}

// KeyframeAtOrBefore returns the latest keyframe time not after ns.
func (ix *SampleIndex) KeyframeAtOrBefore(ns int64) (int64, bool) {
	i := sort.Search(len(ix.KeyframeTimes), func(i int) bool {
		return ix.KeyframeTimes[i] > ns
	})
	if i == 0 {
		return 0, false
	}
	return ix.KeyframeTimes[i-1], true
}

// SampleRange returns the half-open sample index range covering the
// half-open time range [start, end) in nanoseconds.
func (ix *SampleIndex) SampleRange(start, end int64) (from, to int) {
	from = sort.Search(len(ix.SampleTimes), func(i int) bool {
		return ix.SampleTimes[i] >= start
	})
	to = sort.Search(len(ix.SampleTimes), func(i int) bool {
		return ix.SampleTimes[i] >= end
	})
	return from, to
}
