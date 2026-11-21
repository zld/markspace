// Package segment plans segment boundaries snapped to keyframes.
package segment

import (
	"fmt"
	"math"

	"transcode/internal/media"
)

// Segment is a half-open time range [StartSec, EndSec) of the input.
type Segment struct {
	Index    int     `json:"index"`
	StartSec float64 `json:"start_sec"`
	EndSec   float64 `json:"end_sec"`
}

// Duration returns the segment length in seconds.
func (s Segment) Duration() float64 { return s.EndSec - s.StartSec }

// MappingError marks a boundary that cannot be mapped to a keyframe
// reliably; the scheduler records the reason and fails the plan.
type MappingError struct {
	AtSec  float64
	Reason string
}

func (e *MappingError) Error() string {
	return fmt.Sprintf("boundary at %.3fs: %s", e.AtSec, e.Reason)
}

// Plan splits the stream into segments of roughly segSec seconds, with
// every cut on a keyframe. The final segment shorter than one second is
// merged into its predecessor. A boundary with no keyframe within the
// snap tolerance fails the whole plan with a MappingError.
func Plan(info *media.StreamInfo, segSec float64) ([]Segment, error) {
	if segSec <= 0 {
		return nil, fmt.Errorf("segment duration must be positive")
	}
	dur := info.DurationSec
	if dur <= 0 {
		return nil, fmt.Errorf("stream duration unknown")
	}
	if len(info.KeyframeSec) == 0 {
		return nil, &MappingError{AtSec: 0, Reason: "no keyframes found; input not seekable"}
	}
	if info.HasDiscontinuity() {
		return nil, &MappingError{AtSec: 0, Reason: "timestamp discontinuity detected; boundaries not reliable"}
	}
	// Snap tolerance: a keyframe must exist within half a segment of the
	// ideal boundary, otherwise the mapping is ambiguous.
	tol := segSec / 2
	bounds := []float64{0}
	for t := segSec; t < dur-1e-6; t += segSec {
		k, ok := nearestKeyframe(info.KeyframeSec, t, tol)
		if !ok {
			return nil, &MappingError{
				AtSec:  t,
				Reason: fmt.Sprintf("no keyframe within %.2fs; cannot cut on keyframe boundary", tol),
			}
		}
		if k-bounds[len(bounds)-1] < 0.05 {
			continue // keyframe collides with previous cut; skip
		}
		bounds = append(bounds, k)
	}
	bounds = append(bounds, dur)
	segs := make([]Segment, 0, len(bounds)-1)
	for i := 0; i+1 < len(bounds); i++ {
		segs = append(segs, Segment{Index: i, StartSec: bounds[i], EndSec: bounds[i+1]})
	}
	// Merge a trailing sub-second fragment into the previous segment.
	if len(segs) > 1 && segs[len(segs)-1].Duration() < 1.0 {
		segs[len(segs)-2].EndSec = segs[len(segs)-1].EndSec
		segs = segs[:len(segs)-1]
	}
	for i := range segs {
		segs[i].Index = i
	}
	return segs, nil
}

// nearestKeyframe finds the keyframe closest to t. It returns false
// when the closest keyframe is farther than tol.
func nearestKeyframe(keys []float64, t, tol float64) (float64, bool) {
	best := math.Inf(1)
	bestDist := math.Inf(1)
	for _, k := range keys {
		d := math.Abs(k - t)
		if d < bestDist {
			best, bestDist = k, d
		}
	}
	if bestDist > tol {
		return 0, false
	}
	return best, true
}
