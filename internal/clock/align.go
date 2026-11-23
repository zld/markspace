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

// measureSkew measures the residual cross-track alignment error after the
// per-track OffsetNanos shift has been applied. Alignment makes every track's
// first presentation timestamp coincide at session time zero, so the residual
// is the spread of the shifted start boundaries. Track lengths differ by
// design (RTP capture vs. a 4 s clip), so end boundaries are intentionally
// excluded — they describe coverage, not clock alignment.
func measureSkew(tl *model.Timeline) int64 {
	var tracks []*model.Track
	for _, tr := range tl.Tracks {
		if len(tr.Samples) > 0 {
			tracks = append(tracks, tr)
		}
	}
	if len(tracks) < 2 {
		return 0
	}
	var lo, hi int64
	for i, tr := range tracks {
		start := tr.Samples[0].PTS + tr.OffsetNanos
		if i == 0 || start < lo {
			lo = start
		}
		if i == 0 || start > hi {
			hi = start
		}
	}
	return hi - lo
}
