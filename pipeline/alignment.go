// Package pipeline plans and executes segment boundaries over aligned
// representation layers.
package pipeline

import (
	"sort"

	"github.com/zld/mediapipe/index"
	"github.com/zld/mediapipe/media"
)

// PlannedSegment is one output boundary in shared media time. Every
// representation uses the same Start and End, so layers stay aligned.
type PlannedSegment struct {
	Number     int        `json:"number"`
	Start      media.Time `json:"-"`
	End        media.Time `json:"-"`
	StartUS    int64      `json:"start_us"`
	EndUS      int64      `json:"end_us"`
	DurationUS int64      `json:"duration_us"`
	// MaxDeviationUS is the worst distance between the target boundary and the
	// chosen keyframe across representations.
	MaxDeviationUS int64 `json:"max_deviation_us"`
	// LayerBounds records the actual keyframe chosen per representation.
	LayerBounds []LayerBoundary `json:"layer_bounds"`
}

// LayerBoundary is the keyframe chosen for one representation layer.
type LayerBoundary struct {
	Layer       string `json:"layer"`
	Keyframe    int    `json:"keyframe_sample"`
	BoundaryUS  int64  `json:"boundary_us"`
	DeviationUS int64  `json:"deviation_us"`
}

// PlanConfig controls boundary selection.
type PlanConfig struct {
	Segment media.Time
	// MaxDeviation fails the plan when any layer's boundary is further than
	// this from the shared target boundary.
	MaxDeviation media.Time
}

// DefaultMaxDeviation allows one nominal video frame of boundary skew.
func DefaultMaxDeviation(videoFrame media.Time) media.Time {
	if videoFrame > 0 {
		return videoFrame
	}
	return 33 * media.Time(1_000_000)
}

func (c PlanConfig) validate() error {
	if c.Segment <= 0 {
		return media.NewError(media.KindArgument, "", "segment duration must be positive")
	}
	if c.MaxDeviation < 0 {
		return media.NewError(media.KindArgument, "", "max deviation must not be negative")
	}
	return nil
}

// Plan computes shared segment boundaries from the reference index.
//
// Boundaries are cut at the reference video track's keyframes: the start
// target advances by the segment duration, and the chosen boundary is the
// latest keyframe at or before the target, except the final segment which
// ends at the track end. All representations are then mapped onto these
// boundaries; a representation with no keyframe at or before the boundary
// records a deviation and, above threshold, fails the plan.
func Plan(ref *index.TrackIndex, layers []*index.TrackIndex, cfg PlanConfig) ([]PlannedSegment, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if ref == nil {
		return nil, media.NewError(media.KindUnsupported, "", "reference track is missing")
	}
	if len(ref.Keyframes) == 0 {
		return nil, media.NewError(media.KindMissingKeyframe, "", "reference track has no keyframes")
	}
	allLayers := append([]*index.TrackIndex{ref}, layers...)

	// Build canonical layer names.
	for i, l := range allLayers {
		if l == nil {
			return nil, media.NewError(media.KindUnsupported, "", "layer %d is missing", i)
		}
	}

	start := media.Time(ref.StartUS * 1_000)
	natural := media.Time(ref.EndUS * 1_000)
	if natural <= start {
		return nil, media.NewError(media.KindZeroDuration, "", "track has no segmentable span")
	}
	boundaries := []media.Time{start}
	current := start
	for {
		target := current + cfg.Segment
		if target >= natural {
			break
		}
		cut, ok := findKeyframeAtOrBefore(ref, target)
		if !ok {
			// The target precedes the first keyframe; start at the first one.
			if cut, ok = firstKeyframeAfter(ref, current); !ok {
				break
			}
		}
		cutTime := media.Time(cut.TimeUS * 1_000)
		if cutTime <= current {
			// The chosen keyframe is already the previous boundary: advance to
			// the next keyframe so segments never repeat or invert.
			next, ok := firstKeyframeAfter(ref, current)
			if !ok {
				break
			}
			cutTime = media.Time(next.TimeUS * 1_000)
		}
		if cutTime >= natural {
			break
		}
		boundaries = append(boundaries, cutTime)
		current = cutTime
	}
	if natural > boundaries[len(boundaries)-1] {
		boundaries = append(boundaries, natural)
	}

	out := make([]PlannedSegment, 0, len(boundaries)-1)
	for i := 0; i+1 < len(boundaries); i++ {
		segStart, segEnd := boundaries[i], boundaries[i+1]
		if segEnd <= segStart {
			continue
		}
		seg := PlannedSegment{
			Number: len(out) + 1,
			Start:  segStart, End: segEnd,
			StartUS: segStart.Micros(), EndUS: segEnd.Micros(),
			DurationUS: (segEnd - segStart).Micros(),
		}
		for li, layer := range allLayers {
			name := layerName(li, layer)
			bf, ok := findKeyframeAtOrBefore(layer, segStart)
			if !ok {
				return nil, media.NewError(media.KindMissingKeyframe, name,
					"no keyframe at or before %s", segStart)
			}
			dev := (media.Time(bf.TimeUS*1_000) - segStart).Abs()
			seg.LayerBounds = append(seg.LayerBounds, LayerBoundary{
				Layer: name, Keyframe: bf.Sample, BoundaryUS: bf.TimeUS,
				DeviationUS: dev.Micros(),
			})
			if d := dev.Micros(); d > seg.MaxDeviationUS {
				seg.MaxDeviationUS = d
			}
			if dev > cfg.MaxDeviation {
				return nil, media.NewError(media.KindMissingKeyframe, name,
					"boundary deviation %s exceeds limit %s at segment %d",
					dev, cfg.MaxDeviation, seg.Number)
			}
		}
		out = append(out, seg)
	}
	if len(out) == 0 {
		return nil, media.NewError(media.KindZeroDuration, "", "track has no segmentable span")
	}
	// Segments are emitted in boundary order; sorting is defensive and must
	// not mask a planning error, so verify monotonicity explicitly.
	for i := 1; i < len(out); i++ {
		if out[i].StartUS < out[i-1].StartUS || out[i].StartUS != out[i-1].EndUS {
			return nil, media.NewError(media.KindInternal, "", "segments are not contiguous at %d", i)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].StartUS < out[b].StartUS })
	return out, nil
}

func layerName(i int, t *index.TrackIndex) string {
	if i == 0 {
		return "reference"
	}
	if t.Kind != "" {
		return t.Kind
	}
	return "layer"
}

// nearestAtOrBefore returns the latest keyframe time not after target, or the
// first keyframe if the target precedes all of them.
func nearestAtOrBefore(t *index.TrackIndex, target media.Time) media.Time {
	bf, ok := findKeyframeAtOrBefore(t, target)
	if !ok {
		return media.Time(t.Keyframes[0].TimeUS * 1_000)
	}
	return media.Time(bf.TimeUS * 1_000)
}

func findKeyframeAtOrBefore(t *index.TrackIndex, target media.Time) (index.Keyframe, bool) {
	targetUS := target.Micros()
	best := index.Keyframe{}
	found := false
	for _, kf := range t.Keyframes {
		if kf.TimeUS <= targetUS {
			best, found = kf, true
			continue
		}
		break
	}
	return best, found
}

func firstKeyframeAfter(t *index.TrackIndex, after media.Time) (index.Keyframe, bool) {
	afterUS := after.Micros()
	for _, kf := range t.Keyframes {
		if kf.TimeUS > afterUS {
			return kf, true
		}
	}
	return index.Keyframe{}, false
}
