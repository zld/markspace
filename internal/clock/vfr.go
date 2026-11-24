package clock

import (
	"mediad/internal/mederr"
)

// MaxVFRFrames bounds one explicit frame timeline.
const MaxVFRFrames = 1 << 22

// Frame is one explicitly timestamped video frame. VFR durations are
// derived from adjacent PTS values or an explicit final duration; they are
// never estimated from an average frame rate.
type Frame struct {
	Index      int   `json:"index"`
	RawPTS     int64 `json:"raw_pts"`
	PTSNs      int64 `json:"pts_ns"`
	DurationNs int64 `json:"duration_ns"`
}

// NormalizeVFR converts raw PTS values on scale to nanoseconds and derives
// every frame duration from the next explicit PTS. The final frame uses
// lastDurationNs. Non-monotonic PTS and intervals longer than extremeNs are
// added to errs as locatable, recoverable timeline errors and the bad frame
// is skipped; independent tracks can therefore continue processing.
func NormalizeVFR(pts []int64, scale Scale, track string, lastDurationNs, extremeNs int64, errs *mederr.List) []Frame {
	frames := make([]Frame, 0, len(pts))
	if len(pts) > MaxVFRFrames {
		errs.Add(mederr.New(mederr.CatLimit, track, -1, -1, false,
			"frame count %d exceeds limit %d", len(pts), MaxVFRFrames))
		return frames
	}
	if !scale.Valid() {
		errs.Add(mederr.New(mederr.CatTimeline, track, -1, -1, false, "invalid PTS scale %+v", scale))
		return frames
	}
	if lastDurationNs <= 0 {
		errs.Add(mederr.New(mederr.CatTimeline, track, -1, -1, false,
			"final VFR duration must be positive, got %d ns", lastDurationNs))
		return frames
	}

	goodPTS := make([]int64, 0, len(pts))
	goodNanos := make([]int64, 0, len(pts))
	var lastGood int64
	have := false
	for _, raw := range pts {
		ns := scale.ToNanos(raw)
		if ns < 0 {
			errs.Add(mederr.New(mederr.CatTimeline, track, 0, ns, true,
				"negative PTS %d (%d ns)", raw, ns))
			continue
		}
		if have && ns <= lastGood {
			errs.Add(mederr.New(mederr.CatTimeline, track, lastGood, ns, true,
				"non-monotonic VFR PTS: %d ns after %d ns", ns, lastGood))
			continue
		}
		goodPTS = append(goodPTS, raw)
		goodNanos = append(goodNanos, ns)
		lastGood = ns
		have = true
	}

	for i := range goodNanos {
		duration := lastDurationNs
		end := goodNanos[i] + lastDurationNs
		if i+1 < len(goodNanos) {
			duration = goodNanos[i+1] - goodNanos[i]
			end = goodNanos[i+1]
		}
		if duration <= 0 {
			// Defensive: the monotonic filter above normally prevents this.
			errs.Add(mederr.New(mederr.CatTimeline, track, goodNanos[i], end, true,
				"frame %d has non-positive duration %d ns", i, duration))
			continue
		}
		if extremeNs > 0 && duration > extremeNs {
			errs.Add(mederr.New(mederr.CatTimeline, track, goodNanos[i], end, true,
				"extreme VFR interval %.3fms exceeds %.3fms", Millis(duration), Millis(extremeNs)))
		}
		frames = append(frames, Frame{
			Index:      len(frames),
			RawPTS:     goodPTS[i],
			PTSNs:      goodNanos[i],
			DurationNs: duration,
		})
	}
	return frames
}

// EndNs returns the exclusive end of the explicit frame sequence.
func VFREndNs(frames []Frame) int64 {
	if len(frames) == 0 {
		return 0
	}
	f := frames[len(frames)-1]
	return f.PTSNs + f.DurationNs
}

// SnapToSamples rounds a nanosecond boundary to the nearest sample at rate
// Hz and returns the absolute alignment error in nanoseconds. Callers use
// this instead of accumulating resampler phase across chunks.
func SnapToSamples(boundaryNs int64, rate int) (sample int64, snappedNs, errorNs int64) {
	if rate <= 0 {
		return 0, 0, boundaryNs
	}
	// nearest integer sample: round(boundaryNs * rate / 1e9)
	scale := Scale{Num: int64(rate), Den: 1}
	sample = scale.FromNanos(boundaryNs)
	snappedNs = scale.ToNanos(sample)
	diff := snappedNs - boundaryNs
	if diff < 0 {
		diff = -diff
	}
	return sample, snappedNs, diff
}

// MaxSnapErrorNs returns the largest sample-grid rounding error for boundaries.
func MaxSnapErrorNs(boundaries []int64, rate int) int64 {
	var max int64
	for _, b := range boundaries {
		_, _, e := SnapToSamples(b, rate)
		if e > max {
			max = e
		}
	}
	return max
}
