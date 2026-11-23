// Package clock aligns independently timestamped tracks onto one session
// timeline and estimates the device clock drift between them.
package clock

import (
	"math"

	"mediapipe/internal/fault"
	"mediapipe/internal/model"
)

// AlignmentBudgetNanos is the maximum permitted cross-track timeline error.
const AlignmentBudgetNanos int64 = 20_000_000 // 20 ms

// DriftThresholdPPM flags a track whose estimated drift is too large to
// correct silently.
const DriftThresholdPPM = 100.0

// Result describes the alignment computed for one timeline.
type Result struct {
	// MaxSkewNanos is the largest residual cross-track offset after align.
	MaxSkewNanos int64
	// TrackOffsets records the OffsetNanos applied to each track.
	TrackOffsets map[string]int64
	// DriftPPM records each RTP track's estimated drift.
	DriftPPM map[string]float64
}

// Align shifts every track onto a common session axis by matching their
// earliest presentation timestamp to zero, then validates that the residual
// inter-track skew at shared coverage stays within the 20 ms budget.
func Align(tl *model.Timeline, errs *fault.Batch) Result {
	res := Result{
		TrackOffsets: map[string]int64{},
		DriftPPM:     map[string]float64{},
	}
	if len(tl.Tracks) == 0 {
		return res
	}
	// Reference: smallest first-PTS across tracks becomes session time zero.
	var minStart int64 = math.MaxInt64
	for _, tr := range tl.Tracks {
		if len(tr.Samples) == 0 {
			continue
		}
		first := tr.Samples[0].PTS
		if first < minStart {
			minStart = first
		}
	}
	if minStart == math.MaxInt64 {
		return res
	}
	for _, tr := range tl.Tracks {
		if len(tr.Samples) == 0 {
			continue
		}
		// Offset moves the track's earliest PTS onto the shared origin.
		off := minStart - tr.Samples[0].PTS
		tr.OffsetNanos = off
		res.TrackOffsets[tr.ID] = off
		if tr.ClockPpm != 0 {
			res.DriftPPM[tr.ID] = tr.ClockPpm
			if math.Abs(tr.ClockPpm) > DriftThresholdPPM {
				errs.Add(fault.New(fault.KindDrift,
					"clock drift exceeds correction threshold",
					tr.SourceFile, tr.TrackOff, true))
			}
		}
	}
	res.MaxSkewNanos = measureSkew(tl)
	if res.MaxSkewNanos > AlignmentBudgetNanos {
		errs.Add(fault.New(fault.KindInvariant,
			"cross-track alignment exceeds 20ms budget",
			"", -1, true))
	}
	return res
}

// measureSkew compares the session-time positions of samples across tracks at
// matched elapsed fractions of their shared coverage. It returns the largest
// absolute residual in nanoseconds.
func measureSkew(tl *model.Timeline) int64 {
	type span struct{ start, end int64 }
	spans := map[string]span{}
	var tracks []*model.Track
	for _, tr := range tl.Tracks {
		if len(tr.Samples) == 0 {
			continue
		}
		start := tr.Samples[0].PTS + tr.OffsetNanos
		end := tr.Samples[len(tr.Samples)-1].PTS +
			tr.Samples[len(tr.Samples)-1].DurationNanos + tr.OffsetNanos
		spans[tr.ID] = span{start, end}
		tracks = append(tracks, tr)
	}
	if len(tracks) < 2 {
		return 0
	}
	var maxSkew int64
	// Sample at a fixed set of relative positions within each track and
	// compare the resulting session timestamps pairwise.
	const probes = 16
	for p := 1; p < probes; p++ {
		frac := float64(p) / probes
		var times []int64
		for _, tr := range tracks {
			sp := spans[tr.ID]
			target := float64(sp.start) + frac*float64(sp.end-sp.start)
			if t := nearestSessionTime(tr, int64(target)); t >= 0 {
				times = append(times, t)
			}
		}
		if len(times) < 2 {
			continue
		}
		lo, hi := times[0], times[0]
		for _, t := range times[1:] {
			if t < lo {
				lo = t
			}
			if t > hi {
				hi = t
			}
		}
		if skew := hi - lo; skew > maxSkew {
			maxSkew = skew
		}
	}
	return maxSkew
}

// nearestSessionTime returns the session timestamp of the sample closest to
// the requested session time, or -1 when the track has no samples.
func nearestSessionTime(tr *model.Track, target int64) int64 {
	best := int64(-1)
	bestDist := int64(math.MaxInt64)
	for _, s := range tr.Samples {
		st := s.PTS + tr.OffsetNanos
		d := st - target
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			bestDist, best = d, st
		}
	}
	return best
}
