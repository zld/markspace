// Package analyze computes keyframe (GOP) interval distributions from real
// sample durations — never an assumed frame rate — and bounds the media
// range damaged by a corrupted sample using surrounding sync samples.
package analyze

import "mediascan/internal/model"

// Sample is the timeline information analysis needs about one sample.
type Sample struct {
	Index          int64
	TimeStartTicks int64
	DurationTicks  int64
	Sync           bool
}

// GOPStats holds keyframe intervals in unified ticks.
type GOPStats struct {
	SyncTimes []int64
	Intervals []int64
	MinTicks  int64
	MaxTicks  int64
}

// GOP computes intervals between consecutive sync samples using their actual
// timestamps. The caller must scope samples to one continuity period, i.e.
// never span an HLS discontinuity.
func GOP(samples []Sample) GOPStats {
	st := GOPStats{MinTicks: model.Unknown, MaxTicks: model.Unknown}
	for _, s := range samples {
		if s.Sync {
			st.SyncTimes = append(st.SyncTimes, s.TimeStartTicks)
		}
	}
	for i := 1; i < len(st.SyncTimes); i++ {
		v := st.SyncTimes[i] - st.SyncTimes[i-1]
		if v <= 0 {
			continue
		}
		st.Intervals = append(st.Intervals, v)
		if st.MinTicks == model.Unknown || v < st.MinTicks {
			st.MinTicks = v
		}
		if v > st.MaxTicks {
			st.MaxTicks = v
		}
	}
	return st
}

// DamageRange returns the unified-tick interval of the GOP affected by the
// sample at damagedIndex: from the sync sample at or before it to the next
// sync sample at or after it (the decoder recovery point). Missing anchors
// fall back to the first/last sample bounds.
func DamageRange(samples []Sample, damagedIndex int64) (startTicks, endTicks int64) {
	if len(samples) == 0 {
		return model.Unknown, model.Unknown
	}
	if damagedIndex < 0 {
		damagedIndex = 0
	}
	if damagedIndex >= int64(len(samples)) {
		damagedIndex = int64(len(samples)) - 1
	}

	prev := 0
	for i := int64(0); i <= damagedIndex; i++ {
		if samples[i].Sync {
			prev = int(i)
		}
	}
	// Recovery point is the first sync sample strictly after the damaged one.
	next := -1
	for i := int(damagedIndex) + 1; i < len(samples); i++ {
		if samples[i].Sync {
			next = i
			break
		}
	}

	startTicks = samples[prev].TimeStartTicks
	if next >= 0 {
		endTicks = samples[next].TimeStartTicks
	} else {
		last := samples[len(samples)-1]
		endTicks = last.TimeStartTicks + last.DurationTicks
	}
	if endTicks < startTicks {
		endTicks = startTicks
	}
	return startTicks, endTicks
}

// ToAnalyzeSamples converts timestamp-bearing samples using a mapper so the
// package stays free of an mp4 dependency.
func ToAnalyzeSamples(n int, at func(i int) Sample) []Sample {
	out := make([]Sample, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, at(i))
	}
	return out
}
