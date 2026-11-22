// Package align matches keyframe boundaries across representations so every
// bitrate switch lands on the same program time. It never invents keyframes:
// if no common boundary exists within tolerance the material is rejected.
package align

import (
	"fmt"
	"sort"

	"mediacheck/internal/timeline"
)

// Representation is one bitrate layer after timeline normalisation.
type Representation struct {
	ID      string
	Frames  []timeline.Frame // already drift-corrected, monotonic in T0
}

// Boundary is one common segment cut expressed in every representation's own
// frame index plus the unified nanosecond time.
type Boundary struct {
	Index      int
	T0         int64
	FrameIndex map[string]int // representation ID -> frame index at/just before T0
}

// Options controls alignment.
type Options struct {
	TargetSegmentNS int64 // desired segment duration
	MaxSkewNS       int64 // maximum allowed boundary mismatch across layers
}

// MisalignError names the representations and candidate times that could not
// be reconciled, so the operator can diagnose without re-scanning bytes.
type MisalignError struct {
	Reason   string
	Track    string
	WantT0   int64
	Candidates []int64
}

func (e *MisalignError) Error() string {
	return fmt.Sprintf("align: %s track=%s want_t0=%d candidates=%v", e.Reason, e.Track, e.WantT0, e.Candidates)
}

// Aligner computes deterministic common boundaries.
type Aligner struct {
	opt Options
}

// New validates options.
func New(opt Options) (*Aligner, error) {
	if opt.TargetSegmentNS <= 0 {
		return nil, fmt.Errorf("align: target segment duration must be positive")
	}
	if opt.MaxSkewNS < 0 {
		return nil, fmt.Errorf("align: max skew cannot be negative")
	}
	return &Aligner{opt: opt}, nil
}

// keyframeTimes returns the T0 of every keyframe in a representation.
func keyframeTimes(rep Representation) []int64 {
	var out []int64
	for _, f := range rep.Frames {
		if f.Keyframe {
			out = append(out, f.T0)
		}
	}
	return out
}

// nearestKeyframe returns the keyframe time closest to target and its distance.
func nearestKeyframe(keyframes []int64, target int64) (int64, int64) {
	i := sort.Search(len(keyframes), func(i int) bool { return keyframes[i] >= target })
	best := int64(0)
	bestDist := int64(1<<62)
	consider := func(j int) {
		if j < 0 || j >= len(keyframes) {
			return
		}
		d := keyframes[j] - target
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			bestDist, best = d, keyframes[j]
		}
	}
	consider(i - 1)
	consider(i)
	return best, bestDist
}

// Align builds the common boundary list. Every cut is anchored on a real
// keyframe of every representation within MaxSkewNS; otherwise alignment
// fails closed with the offending candidates.
func (a *Aligner) Align(reps []Representation) ([]Boundary, error) {
	if len(reps) == 0 {
		return nil, fmt.Errorf("align: no representations")
	}
	kf := make(map[string][]int64, len(reps))
	var globalEnd int64 = -1
	for _, rep := range reps {
		if len(rep.Frames) == 0 {
			return nil, &MisalignError{Reason: "empty representation", Track: rep.ID}
		}
		times := keyframeTimes(rep)
		if len(times) == 0 {
			return nil, &MisalignError{Reason: "no keyframe", Track: rep.ID}
		}
		kf[rep.ID] = times
		end := rep.Frames[len(rep.Frames)-1].T0
		if globalEnd < 0 || end < globalEnd {
			globalEnd = end
		}
	}

	// Anchor the grid on the earliest shared start (first frame of each layer)
	// and walk forward by the target duration.
	startGrid := reps[0].Frames[0].T0
	for _, rep := range reps {
		if t := rep.Frames[0].T0; t > startGrid {
			startGrid = t
		}
	}
	var boundaries []Boundary
	grid := startGrid
	idx := 0
	for grid <= globalEnd {
		b := Boundary{Index: idx, FrameIndex: map[string]int{}, T0: grid}
		chosen := grid
		first := true
		for _, rep := range reps {
			t, dist := nearestKeyframe(kf[rep.ID], grid)
			if dist > a.opt.MaxSkewNS {
				return nil, &MisalignError{
					Reason: "no common keyframe within skew", Track: rep.ID,
					WantT0: grid, Candidates: nearbyCandidates(kf[rep.ID], grid),
				}
			}
			// All layers must converge on one boundary time; take the first
			// layer's keyframe and require the others to match within skew.
			if first {
				chosen = t
				first = false
			} else {
				d := t - chosen
				if d < 0 {
					d = -d
				}
				if d > a.opt.MaxSkewNS {
					return nil, &MisalignError{
						Reason: "cross-layer keyframe skew too large", Track: rep.ID,
						WantT0: chosen, Candidates: nearbyCandidates(kf[rep.ID], chosen),
					}
				}
			}
			b.FrameIndex[rep.ID] = frameIndexAtOrBefore(rep.Frames, chosen)
		}
		b.T0 = chosen
		boundaries = append(boundaries, b)
		idx++
		grid += a.opt.TargetSegmentNS
	}
	if len(boundaries) < 2 {
		return nil, fmt.Errorf("align: material too short for two boundaries")
	}
	return boundaries, nil
}

// frameIndexAtOrBefore returns the last frame index whose T0 <= t.
func frameIndexAtOrBefore(frames []timeline.Frame, t int64) int {
	i := sort.Search(len(frames), func(i int) bool { return frames[i].T0 > t })
	return i - 1
}

func nearbyCandidates(keyframes []int64, target int64) []int64 {
	i := sort.Search(len(keyframes), func(i int) bool { return keyframes[i] >= target })
	var out []int64
	for j := i - 2; j <= i+2; j++ {
		if j >= 0 && j < len(keyframes) {
			out = append(out, keyframes[j])
		}
	}
	return out
}

// SegmentDurations returns the nanosecond duration of each segment derived
// from consecutive boundaries (last segment runs to the final frame).
func SegmentDurations(boundaries []Boundary, endT0 int64) []int64 {
	if len(boundaries) == 0 {
		return nil
	}
	durs := make([]int64, 0, len(boundaries))
	for i := 0; i+1 < len(boundaries); i++ {
		durs = append(durs, boundaries[i+1].T0-boundaries[i].T0)
	}
	durs = append(durs, endT0-boundaries[len(boundaries)-1].T0)
	return durs
}
