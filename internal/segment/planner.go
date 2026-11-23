// Package segment plans keyframe-aligned segments and aligns them
// across representations.
package segment

import (
	"sort"

	"mediqc/internal/fault"
)

// KeyframeSource records where the sync-sample positions came from.
type KeyframeSource string

const (
	SourceIndex KeyframeSource = "stss"        // container sync-sample index
	SourceScan  KeyframeSource = "sample-scan" // samples scanned, index missing
)

// Segment is a half-open frame interval [StartFrame, EndFrame).
type Segment struct {
	Index       int
	StartFrame  int64
	EndFrame    int64
	KeyframeHit bool // StartFrame coincides with a real keyframe
}

// Frames returns the segment length in frames.
func (s Segment) Frames() int64 { return s.EndFrame - s.StartFrame }

// Plan cuts [0,totalFrames) into segments of about targetFrames, breaking
// only at keyframes. keyframes must be sorted ascending and start at 0.
func Plan(keyframes []int64, totalFrames, targetFrames int64) ([]Segment, error) {
	if len(keyframes) == 0 || keyframes[0] != 0 {
		return nil, fault.New(fault.CodeMissingKeyframe, 0, -1,
			"keyframe list empty or does not start at frame 0",
			"scan samples to rebuild the sync index")
	}
	if totalFrames <= 0 || targetFrames <= 0 {
		return nil, fault.New(fault.CodeFrameOverflow, 0, -1,
			"totalFrames and targetFrames must be positive",
			"check the asset duration and segment target")
	}
	kfs := append([]int64(nil), keyframes...)
	if !sort.SliceIsSorted(kfs, func(i, j int) bool { return kfs[i] < kfs[j] }) {
		sort.Slice(kfs, func(i, j int) bool { return kfs[i] < kfs[j] })
	}
	var segs []Segment
	start := kfs[0]
	for start < totalFrames {
		target := start + targetFrames
		end := totalFrames
		if target < totalFrames {
			// Cut at the last keyframe <= target; if none exists beyond
			// start, overshoot to the first keyframe after target.
			cut := int64(-1)
			for _, k := range kfs {
				if k <= start {
					continue
				}
				if k <= target {
					cut = k
					continue
				}
				if cut < 0 {
					cut = k
				}
				break
			}
			if cut > 0 {
				end = cut
			}
		}
		hit := containsFrame(kfs, start)
		segs = append(segs, Segment{
			Index:       len(segs),
			StartFrame:  start,
			EndFrame:    end,
			KeyframeHit: hit,
		})
		start = end
	}
	return segs, nil
}

func containsFrame(kfs []int64, f int64) bool {
	i := sort.Search(len(kfs), func(i int) bool { return kfs[i] >= f })
	return i < len(kfs) && kfs[i] == f
}

// KeyframeHitRate is the fraction of segment starts on real keyframes.
func KeyframeHitRate(segs []Segment) float64 {
	if len(segs) == 0 {
		return 0
	}
	hits := 0
	for _, s := range segs {
		if s.KeyframeHit {
			hits++
		}
	}
	return float64(hits) / float64(len(segs))
}
