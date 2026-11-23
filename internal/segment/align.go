package segment

import (
	"fmt"

	"mediqc/internal/clock"
	"mediqc/internal/fault"
)

// Representation is one rung of the bitrate ladder.
type Representation struct {
	Name      string
	Rate      clock.Rate
	Keyframes []int64 // sync-sample frame numbers at Rate
}

// AlignedCut is one segment boundary mapped into every representation.
type AlignedCut struct {
	Tick   int64            // boundary on the unified timeline
	Frames map[string]int64 // representation name -> keyframe used
}

// Align derives shared cut points from the anchor representation (reps[0])
// and snaps every other representation to its nearest keyframe. A
// representation whose nearest keyframe deviates by more than one frame
// fails the alignment.
func Align(reps []Representation, totalTicks, targetTicks int64) ([]AlignedCut, error) {
	if len(reps) == 0 {
		return nil, fault.New(fault.CodeMisalignedCut, 0, -1,
			"no representations", "provide at least one bitrate rung")
	}
	anchor := reps[0]
	totalFrames := clock.TicksToFrames(totalTicks, anchor.Rate)
	targetFrames := clock.TicksToFrames(targetTicks, anchor.Rate)
	if targetFrames < 1 {
		targetFrames = 1
	}
	segs, err := Plan(anchor.Keyframes, totalFrames, targetFrames)
	if err != nil {
		return nil, err
	}
	cuts := make([]AlignedCut, 0, len(segs)+1)
	boundaries := make([]int64, 0, len(segs)+1)
	for _, s := range segs {
		boundaries = append(boundaries, s.StartFrame)
	}
	boundaries = append(boundaries, totalFrames)
	for i, bf := range boundaries {
		tick := clock.FramesToTicks(bf, anchor.Rate)
		cut := AlignedCut{Tick: tick, Frames: map[string]int64{anchor.Name: bf}}
		last := i == len(boundaries)-1
		for _, rep := range reps[1:] {
			ideal := clock.TicksToFrames(tick, rep.Rate)
			if last {
				// Asset end needs no keyframe.
				cut.Frames[rep.Name] = ideal
				continue
			}
			k, dev, ok := nearestKeyframe(rep.Keyframes, ideal)
			if !ok || dev > 1 {
				return nil, fault.New(fault.CodeMisalignedCut, 0, tick,
					fmt.Sprintf("representation %s deviates %d frames at tick %d",
						rep.Name, dev, tick),
					"force aligned GOPs across the ladder")
			}
			cut.Frames[rep.Name] = k
		}
		cuts = append(cuts, cut)
	}
	return cuts, nil
}

// nearestKeyframe finds the keyframe closest to frame f.
func nearestKeyframe(kfs []int64, f int64) (k, dev int64, ok bool) {
	if len(kfs) == 0 {
		return 0, 0, false
	}
	best, bestDev := int64(0), int64(-1)
	for _, kf := range kfs {
		d := kf - f
		if d < 0 {
			d = -d
		}
		if bestDev < 0 || d < bestDev {
			best, bestDev = kf, d
		}
	}
	return best, bestDev, true
}
