// Package segment aligns multi-bitrate representations onto the common
// timeline, plans switchable boundaries at shared keyframes, computes per
// segment bitrate statistics and emits deterministic manifests.
package segment

import (
	"fmt"
	"sort"

	"mediadist/timeline"
)

// Config holds the alignment and conformance tolerances. All time fields are
// common-clock ticks (90 kHz).
type Config struct {
	// TargetSegmentTicks is the nominal segment duration on the grid.
	TargetSegmentTicks uint64
	// MaxKeyframeDriftTicks is the largest allowed keyframe-to-grid offset.
	MaxKeyframeDriftTicks uint64
	// MaxDurationErrorTicks bounds manifest-duration vs timeline-end error.
	MaxDurationErrorTicks uint64
	// MaxBitrateErrorBasisPts bounds target/actual bitrate deviation
	// (1000 basis points = 10%, 1500 = 15%).
	MaxBitrateErrorBasisPts uint64
}

// DefaultConfig matches the acceptance criteria: 2 s segments, 100 ms keyframe
// drift, 20 ms duration conservation and 15% bitrate conformance.
func DefaultConfig() Config {
	return Config{
		TargetSegmentTicks:      2 * uint64(timeline.CommonTimescale),
		MaxKeyframeDriftTicks:   uint64(timeline.CommonTimescale) / 10,
		MaxDurationErrorTicks:   uint64(timeline.CommonTimescale) / 50,
		MaxBitrateErrorBasisPts: 1500,
	}
}

// Representation binds one bitrate tier to its parsed tracks.
type Representation struct {
	ID               string
	TargetBitrateBps uint64
	Video            *timeline.Track
	Audio            *timeline.Track
}

// Boundary is one point on the shared timeline. The grid time never moves
// (audio stays put); per-representation keyframe offsets decide switchability.
type Boundary struct {
	At         uint64
	Switchable bool
	// KeyframeDTS maps representation ID to the keyframe DTS nearest to At.
	KeyframeDTS map[string]uint64
	// MaxDrift is the largest keyframe-to-grid offset across representations.
	MaxDrift uint64
}

// Plan is the alignment result shared by every representation.
type Plan struct {
	End        uint64
	Boundaries []Boundary
}

// Align computes the common boundary set. A boundary is switchable only when
// every representation has a keyframe within MaxKeyframeDriftTicks of the grid
// time. Audio timestamps are never shifted. A representation lacking any
// interior keyframe produces KEYFRAME_MISSING with stage and track locators.
func Align(reps []Representation, cfg Config) (*Plan, error) {
	if len(reps) == 0 {
		return nil, timeline.NewLocError(timeline.CodeBadArgument, timeline.StageSegment, 0, -1, "align",
			fmt.Errorf("no representations"))
	}
	var end uint64
	for _, r := range reps {
		if r.Video == nil {
			return nil, timeline.NewLocError(timeline.CodeBadArgument, timeline.StageSegment, 0, -1,
				"align", fmt.Errorf("representation %s has no video track", r.ID))
		}
		if e := r.Video.End(); e > end {
			end = e
		}
	}
	if err := assertInteriorKeyframes(reps); err != nil {
		return nil, err
	}
	grid := makeGrid(end, cfg.TargetSegmentTicks)
	plan := &Plan{End: end}
	for _, at := range grid {
		b := Boundary{At: at, Switchable: true, KeyframeDTS: map[string]uint64{}}
		for _, r := range reps {
			dts, drift, ok := nearestKeyframe(r.Video, at, cfg.MaxKeyframeDriftTicks)
			b.KeyframeDTS[r.ID] = dts
			if drift > b.MaxDrift {
				b.MaxDrift = drift
			}
			if !ok {
				// Mark non-switchable; the grid boundary itself is unchanged so
				// the audio timeline is never silently moved.
				b.Switchable = false
			}
		}
		plan.Boundaries = append(plan.Boundaries, b)
	}
	return plan, nil
}

// makeGrid returns boundary times 0, step, 2*step, ... ending exactly at end.
// The last interior boundary snaps to end when the remaining span is shorter
// than one nominal segment, absorbing the remainder into the final segment
// instead of emitting a near-empty sliver that distorts bitrate statistics.
func makeGrid(end, step uint64) []uint64 {
	if step == 0 {
		return []uint64{0, end}
	}
	var out []uint64
	for at := uint64(0); at+step <= end; at += step {
		out = append(out, at)
	}
	if len(out) == 0 {
		return []uint64{0, end}
	}
	last := out[len(out)-1]
	if last == end {
		return out
	}
	out = append(out, end)
	return out
}

// nearestKeyframe finds the keyframe DTS closest to at. ok is false when the
// closest keyframe is beyond the permitted drift.
func nearestKeyframe(v *timeline.Track, at, maxDrift uint64) (dts, drift uint64, ok bool) {
	best := uint64(0)
	found := false
	for _, s := range v.Samples {
		if !s.Keyframe {
			continue
		}
		if !found || absDiff(s.DTS, at) < absDiff(best, at) {
			best, found = s.DTS, true
		}
	}
	if !found {
		return 0, 0, false
	}
	drift = absDiff(best, at)
	return best, drift, drift <= maxDrift
}

func assertInteriorKeyframes(reps []Representation) error {
	for _, r := range reps {
		interior := 0
		for i, s := range r.Video.Samples {
			if i > 0 && s.Keyframe {
				interior++
			}
		}
		if interior == 0 {
			return timeline.NewLocError(timeline.CodeMissingKeyframe, timeline.StageSegment,
				r.Video.ID, -1, "keyframes",
				fmt.Errorf("representation %s track %d has no interior sync samples", r.ID, r.Video.ID))
		}
	}
	return nil
}

func absDiff(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

// BoundarySetEqual reports that two plans expose the identical grid times and
// switchability flags — the representation boundary-set consistency invariant.
func BoundarySetEqual(a, b *Plan) bool {
	if len(a.Boundaries) != len(b.Boundaries) {
		return false
	}
	for i := range a.Boundaries {
		if a.Boundaries[i].At != b.Boundaries[i].At ||
			a.Boundaries[i].Switchable != b.Boundaries[i].Switchable {
			return false
		}
	}
	return true
}

// SortedRepresentationIDs returns representation IDs deterministically.
func SortedRepresentationIDs(reps []Representation) []string {
	ids := make([]string, 0, len(reps))
	for _, r := range reps {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	return ids
}
