package stream

import (
	"sort"

	"media-check/internal/clock"
)

// SyncError summarizes cross-track audio/video synchronization using only
// exact rational times; the final millisecond figure is rounded once.
type SyncError struct {
	// MaxAbsMillis is the largest absolute video-vs-audio offset observed.
	MaxAbsMillis int64
	// P95Millis is the 95th-percentile absolute offset in milliseconds.
	P95Millis int64
	// Compared is the number of video samples evaluated.
	Compared int
}

// ComputeSyncError matches every parsed video sample to the nearest parsed
// audio sample on the unified time base and reports the offset distribution.
// Missing samples (loss/corruption) are simply absent from both sides, so
// the measure stays honest about what was actually recovered.
func ComputeSyncError(rep *Report) (SyncError, bool) {
	v := rep.VideoTrack()
	a := rep.AudioTrack()
	if v == nil || a == nil || len(v.Samples) == 0 || len(a.Samples) == 0 {
		return SyncError{}, false
	}
	audioTimes := make([]clock.Rational, 0, len(a.Samples))
	for _, s := range a.Samples {
		audioTimes = append(audioTimes, s.Time)
	}
	offsets := make([]int64, 0, len(v.Samples))
	for _, vs := range v.Samples {
		nearest := nearestRational(audioTimes, vs.Time)
		offsets = append(offsets, vs.Time.Sub(nearest).Abs().Millis())
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })
	return SyncError{
		MaxAbsMillis: offsets[len(offsets)-1],
		P95Millis:    percentile(offsets, 95),
		Compared:     len(offsets),
	}, true
}

// nearestRational returns the element of the sorted slice closest to target
// using exact rational comparison.
func nearestRational(sorted []clock.Rational, target clock.Rational) clock.Rational {
	idx := sort.Search(len(sorted), func(i int) bool {
		return sorted[i].Cmp(target) >= 0
	})
	switch {
	case idx == 0:
		return sorted[0]
	case idx == len(sorted):
		return sorted[len(sorted)-1]
	default:
		down := target.Sub(sorted[idx-1]).Abs()
		up := sorted[idx].Sub(target).Abs()
		if down.Cmp(up) <= 0 {
			return sorted[idx-1]
		}
		return sorted[idx]
	}
}

// percentile returns the p-th percentile (0..100) using nearest-rank on the
// already-sorted values.
func percentile(sortedAsc []int64, p int) int64 {
	if len(sortedAsc) == 0 {
		return 0
	}
	rank := (p*len(sortedAsc) + 99) / 100 // ceil(p*n/100), 1-based
	if rank < 1 {
		rank = 1
	}
	if rank > len(sortedAsc) {
		rank = len(sortedAsc)
	}
	return sortedAsc[rank-1]
}
