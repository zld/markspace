package syncer

import (
	"sort"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

// Sync aligns the supplied video and audio tracks. The offset is the median
// difference between corresponding sample times after both tracks are made
// monotonic; the residual is the largest remaining deviation after applying
// that offset.
func Sync(video, audio Track, cfg Config) (*Result, error) {
	if len(video.Samples) == 0 {
		return nil, fault.Newf(fault.CodeSyncEmptyTrack, video.Kind, "video track has no samples")
	}
	if len(audio.Samples) == 0 {
		return nil, fault.Newf(fault.CodeSyncEmptyTrack, audio.Kind, "audio track has no samples")
	}
	res := &Result{}
	vSamples, vStats, vFaults := admit(video, cfg)
	aSamples, aStats, aFaults := admit(audio, cfg)
	res.Faults = append(res.Faults, vFaults...)
	res.Faults = append(res.Faults, aFaults...)
	res.Video = vStats
	res.Audio = aStats
	if len(vSamples) == 0 || len(aSamples) == 0 {
		res.Monotonicity = true
		return res, nil
	}
	res.Monotonicity = vStats.Clamped == 0 && aStats.Clamped == 0
	// Pair every video sample with the audio sample nearest in absolute time
	// and take the median difference. Using absolute times keeps a constant
	// offset visible even when the two tracks start at different instants.
	deltas := make([]clock.Time, 0, len(vSamples))
	ai := 0
	for _, v := range vSamples {
		best := abs(aSamples[ai] - v)
		for ai+1 < len(aSamples) {
			d := abs(aSamples[ai+1] - v)
			if d > best {
				break
			}
			ai++
			best = d
		}
		// The audio sample nearest in absolute time is the best available
		// correspondence between the two tracks.
		deltas = append(deltas, aSamples[ai]-v)
	}
	res.Offset = median(deltas)
	res.Samples = len(vSamples) + len(aSamples)
	if abs(res.Offset) > cfg.MaxOffset {
		res.Faults = append(res.Faults, fault.New(fault.CodeSyncLateFrame, 0, "",
			"alignment offset %s exceeds the %s limit", res.Offset, cfg.MaxOffset))
		res.AppliedOffset = 0
	} else {
		res.AppliedOffset = res.Offset
	}
	for _, d := range deltas {
		if r := abs(d - res.AppliedOffset); r > res.Residual {
			res.Residual = r
		}
	}
	return res, nil
}

// admit normalises one track: timestamps are made non-decreasing, late
// samples are clamped (counted) and samples past the tolerance are dropped
// with a fault.
func admit(t Track, cfg Config) ([]clock.Time, TrackStats, []*fault.Fault) {
	stats := TrackStats{Kind: t.Kind, Input: len(t.Samples)}
	var out []clock.Time
	var faults []*fault.Fault
	var last clock.Time
	have := false
	for _, s := range t.Samples {
		if !have {
			last = s.PTS
			have = true
		}
		if s.PTS < last {
			behind := last - s.PTS
			stats.Clamped++
			if behind > cfg.LateTolerance {
				stats.LateDropped++
				faults = append(faults, fault.New(fault.CodeSyncLateFrame, s.PTS.Nanos(), t.Kind,
					"%s sample at %s is %s late (tolerance %s)", t.Kind, s.PTS, behind, cfg.LateTolerance))
			}
			s.PTS = last // never emit a backwards timestamp
		}
		last = s.PTS
		out = append(out, s.PTS)
		stats.Admitted++
	}
	if len(out) > 0 {
		stats.First = out[0]
		stats.Last = out[len(out)-1]
		stats.Duration = stats.Last - stats.First
	}
	return out, stats, faults
}

func median(v []clock.Time) clock.Time {
	c := make([]clock.Time, len(v))
	copy(c, v)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

func abs(t clock.Time) clock.Time {
	if t < 0 {
		return -t
	}
	return t
}
