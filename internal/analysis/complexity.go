// Package analysis samples decoded frames and scores per-segment visual
// complexity with Gonum statistics. All scores are deterministic for a given
// source: no timing or OS randomness is involved.
package analysis

import (
	"context"
	"fmt"
	"math"

	"gonum.org/v1/gonum/stat"

	"bitrack/internal/media"
	"bitrack/internal/model"
)

// FrameRange is a frame-aligned slice of the source. The final range may be a
// short tail; it still covers real source time and must receive budget.
type FrameRange struct {
	Index      int
	StartFrame int
	FrameCount int
}

// BuildRanges splits totalFrames into frame-aligned ranges of at most
// segmentFrames. A non-empty tail is returned as its own range.
func BuildRanges(totalFrames, segmentFrames int) ([]FrameRange, error) {
	if totalFrames <= 0 {
		return nil, fmt.Errorf("segmentation: total frames must be positive, got %d", totalFrames)
	}
	if segmentFrames <= 0 {
		return nil, fmt.Errorf("segmentation: segment length must be positive, got %d", segmentFrames)
	}
	var out []FrameRange
	for start, idx := 0, 0; start < totalFrames; start, idx = start+segmentFrames, idx+1 {
		n := segmentFrames
		if start+n > totalFrames {
			n = totalFrames - start
		}
		out = append(out, FrameRange{Index: idx, StartFrame: start, FrameCount: n})
	}
	return out, nil
}

// AnalyzeComplexity decodes each range (sampling every sampleStride frames)
// and returns one complexity score per range. The score blends spatial
// gradient energy with inter-frame difference, summarized via Gonum.
func AnalyzeComplexity(ctx context.Context, path string, info model.ProbeInfo,
	ranges []FrameRange, sampleStride int) ([]float64, error) {
	if sampleStride <= 0 {
		sampleStride = 1
	}
	scores := make([]float64, len(ranges))
	for _, r := range ranges {
		frames, err := media.DecodeFrames(ctx, path, info.Width, info.Height, r.StartFrame, r.FrameCount)
		if err != nil {
			return nil, fmt.Errorf("complexity: segment %d: %w", r.Index, err)
		}
		metrics := make([]float64, 0, len(frames))
		var prev []byte
		sampled := 0
		for i, frame := range frames {
			if i%sampleStride != 0 {
				continue
			}
			sampled++
			spatial := meanGradient(frame, info.Width, info.Height)
			temporal := 0.0
			if prev != nil {
				temporal = meanAbsDiff(prev, frame)
			}
			metrics = append(metrics, spatial+temporal)
			prev = frame
		}
		if sampled == 0 {
			return nil, fmt.Errorf("complexity: segment %d produced no samples", r.Index)
		}
		mean, variance := stat.MeanVariance(metrics, nil)
		// Mean energy dominates; standard deviation adds reward for segments
		// whose complexity varies strongly inside the range.
		scores[r.Index] = mean + sqrtPositive(variance)
	}
	return scores, nil
}

// meanGradient computes the mean absolute luma difference between horizontal
// and vertical neighbors, a cheap spatial-detail proxy.
func meanGradient(frame []byte, w, h int) float64 {
	sum, n := 0.0, 0
	for y := 0; y+1 < h; y++ {
		row := y * w
		for x := 0; x+1 < w; x++ {
			dx := int(frame[row+x]) - int(frame[row+x+1])
			dy := int(frame[row+x]) - int(frame[row+w+x])
			if dx < 0 {
				dx = -dx
			}
			if dy < 0 {
				dy = -dy
			}
			sum += float64(dx + dy)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// meanAbsDiff is the mean per-pixel luma difference of two equal-size frames.
func meanAbsDiff(a, b []byte) float64 {
	sum := 0.0
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		sum += float64(d)
	}
	return sum / float64(len(a))
}

func sqrtPositive(v float64) float64 {
	if v <= 0 {
		return 0
	}
	return math.Sqrt(v)
}
