// Package planner evaluates candidate processing parameters against a
// resource budget before any work starts, and resolves audio/color
// parameter conflicts into an explicit conversion plan.
package planner

import (
	"fmt"

	"continuity/media"
)

// Constraints are the explicit, operator-supplied processing limits and
// target parameters.
type Constraints struct {
	MaxBufferBytes   int64
	MaxConcurrency   int
	TargetChannels   uint16 // 0 = inputs must already agree
	TargetSampleRate uint32 // 0 = inputs must already agree
	ColorOutput      *media.ColorMeta
}

// AudioConversion is the planned conversion for one audio track.
type AudioConversion struct {
	TrackID      int    `json:"track_id"`
	FromChannels uint16 `json:"from_channels"`
	ToChannels   uint16 `json:"to_channels"`
	FromRate     uint32 `json:"from_rate"`
	ToRate       uint32 `json:"to_rate"`
}

// audioRules lists the channel-layout conversions the kernel has mapping
// rules for. Anything not listed is refused rather than silently
// dropping channels.
var audioRules = map[[2]uint16]bool{
	{1, 1}: true, {2, 2}: true, {6, 6}: true,
	{1, 2}: true, {2, 1}: true, {6, 2}: true,
}

// Rejection explains why one candidate parameter set was refused.
type Rejection struct {
	SegmentNanos int64  `json:"segment_nanos"`
	Reason       string `json:"reason"`
}

// Plan is the approved processing plan.
type Plan struct {
	SegmentNanos int64             `json:"segment_nanos"`
	Concurrency  int               `json:"concurrency"`
	Cuts         []int64           `json:"cuts"` // segment boundaries, nanoseconds
	Audio        []AudioConversion `json:"audio"`
	ColorSources []media.ColorMeta `json:"color_sources"` // per input
	ColorOutput  media.ColorMeta   `json:"color_output"`
	EstBytes     int64             `json:"est_bytes"`
	PlanHash     string            `json:"plan_hash"`
}

// estimateBytes approximates the transient memory of one candidate:
// every input's sample bytes are read and written once, plus a fixed
// per-segment bookkeeping overhead.
func estimateBytes(inputs []*media.Container, segments int) int64 {
	var sampleBytes int64
	for _, c := range inputs {
		for _, t := range c.Tracks {
			for _, s := range t.SampleSizes {
				sampleBytes += int64(s)
			}
		}
	}
	return 2*sampleBytes + int64(segments)*int64(len(inputs))*4096
}

// Evaluate checks candidates against the budget and resolves parameter
// conflicts. On budget failure it returns every rejected candidate with
// its reason and never produces a partial plan.
func Evaluate(inputs []*media.Container, segmentNanos int64, c Constraints) (*Plan, []Rejection, error) {
	if len(inputs) == 0 {
		return nil, nil, &media.Error{Kind: media.KindBudget, Op: "planner.evaluate",
			Msg: "no inputs"}
	}
	if c.MaxConcurrency < 1 || c.MaxBufferBytes <= 0 {
		return nil, nil, &media.Error{Kind: media.KindBudget, Op: "planner.evaluate",
			Msg: "buffer limit and concurrency must be positive"}
	}

	// Common program duration: the shortest video track.
	var durNanos int64 = -1
	for _, in := range inputs {
		v := in.VideoTrack()
		if v == nil {
			return nil, nil, &media.Error{Kind: media.KindParamConflict,
				Op: "planner.evaluate", Msg: "input without a video track: " + in.Path}
		}
		if durNanos < 0 || v.DurationNanos < durNanos {
			durNanos = v.DurationNanos
		}
	}

	// Candidates: the requested segment length, then half and double.
	var rejections []Rejection
	var chosen int64 = -1
	var est int64
	for _, cand := range []int64{segmentNanos, segmentNanos / 2, segmentNanos * 2} {
		if cand <= 0 {
			rejections = append(rejections, Rejection{cand, "non-positive segment duration"})
			continue
		}
		segments := int((durNanos + cand - 1) / cand)
		e := estimateBytes(inputs, segments)
		if e > c.MaxBufferBytes {
			rejections = append(rejections, Rejection{cand,
				fmt.Sprintf("estimated %d bytes exceeds buffer limit %d", e, c.MaxBufferBytes)})
			continue
		}
		chosen, est = cand, e
		break
	}
	if chosen < 0 {
		return nil, rejections, &media.Error{Kind: media.KindBudget,
			Op: "planner.evaluate", Msg: "no candidate segment duration fits the budget"}
	}

	p := &Plan{SegmentNanos: chosen, EstBytes: est}
	for cut := int64(0); cut < durNanos; cut += chosen {
		p.Cuts = append(p.Cuts, cut)
	}
	p.Cuts = append(p.Cuts, durNanos)
	p.Concurrency = c.MaxConcurrency
	if n := len(p.Cuts) - 1; n < p.Concurrency {
		p.Concurrency = n
	}

	if err := p.resolveAudio(inputs, c); err != nil {
		return nil, nil, err
	}
	if err := p.resolveColor(inputs, c); err != nil {
		return nil, nil, err
	}
	p.PlanHash = p.hash(inputs)
	return p, nil, nil
}
