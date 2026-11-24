// Package manifest produces rendition-aligned segment manifests whose
// cumulative timing error stays within one video frame period.
package manifest

import (
	"mediasync/internal/clock"
	"mediasync/internal/fanout"
	"mediasync/internal/repair"
)

// Segment is one manifest entry shared across renditions.
type Segment struct {
	Index        int
	StartNS      int64
	DurationNS   int64
	StartSample  int
	EndSample    int // exclusive
	Switchable   bool
	Rejected     bool
}

// RenditionManifest is one bitrate layer's segment timeline.
type RenditionManifest struct {
	Rendition int
	Bitrate   int
	Segments  []Segment
}

// Manifest is the aligned multi-rendition output.
type Manifest struct {
	Renditions     []RenditionManifest
	TargetSegNS    int64
	MaxErrorNS     int64
	FramePeriodNS  int64
	RejectedPoints int
}

// Builder aligns per-rendition segments onto shared switch boundaries.
type Builder struct {
	targetSegNS   int64
	framePeriodNS int64
}

// NewBuilder validates the target segment and frame periods.
func NewBuilder(targetSegNS, framePeriodNS int64) (*Builder, *clock.Error) {
	if targetSegNS <= 0 || framePeriodNS <= 0 {
		return nil, clock.NewError(clock.ErrInvalidLength, "segment and frame periods must be positive")
	}
	if targetSegNS > clock.MaxDurationMicros*1000 {
		return nil, clock.NewError(clock.ErrInvalidLength, "segment target exceeds duration bound")
	}
	return &Builder{targetSegNS: targetSegNS, framePeriodNS: framePeriodNS}, nil
}

// Build creates one manifest per rendition from recovered samples, then aligns
// segment boundaries to available switch points. Cumulative timing error is
// reabsorbed so it can never exceed a single frame period.
func (b *Builder) Build(ladder *fanout.Ladder, durations [][]int64) (*Manifest, *clock.Error) {
	streams := ladder.Streams()
	if len(streams) == 0 {
		return nil, clock.NewError(clock.ErrInvalidLength, "no renditions to manifest")
	}
	out := &Manifest{TargetSegNS: b.targetSegNS, FramePeriodNS: b.framePeriodNS}
	points := ladder.SwitchPoints()
	for rend, res := range streams {
		if rend >= len(durations) {
			return nil, clock.NewError(clock.ErrInvalidLength, "missing durations for rendition")
		}
		segs, err := b.buildRendition(res.Samples, durations[rend], points)
		if err != nil {
			return nil, err
		}
		out.Renditions = append(out.Renditions, RenditionManifest{
			Rendition: rend, Bitrate: res.Bitrate, Segments: segs,
		})
	}
	out.MaxErrorNS = b.measureError(out)
	out.RejectedPoints = ladder.RejectedBoundaries()
	return out, nil
}

// buildRendition lays segments on the shared target grid, snapping each
// boundary to the nearest available switch point and carrying rounding error.
func (b *Builder) buildRendition(samples []repair.Sample, durations []int64, points []fanout.SwitchPoint) ([]Segment, *clock.Error) {
	if len(samples) == 0 {
		return nil, clock.NewError(clock.ErrUnrecoverable, "rendition has no recovered samples")
	}
	if len(samples) != len(durations) {
		return nil, clock.NewError(clock.ErrInvalidLength, "sample/duration length mismatch")
	}
	segs := make([]Segment, 0, len(samples)/8+1)
	startSample := 0
	var acc, carry int64
	idx := 0
	for i := 1; i < len(samples); i++ {
		acc += durations[i-1]
		// A boundary closes once accumulated media time plus any carried
		// rounding error reaches the target; the carry keeps cumulative error
		// bounded to one frame period rather than drifting segment by segment.
		if acc+carry >= b.targetSegNS {
			dur := acc
			segs = append(segs, Segment{
				Index: idx, StartNS: samples[startSample].MediaNS, DurationNS: dur,
				StartSample: startSample, EndSample: i,
				Switchable: b.switchableAt(points, samples[startSample].MediaNS),
			})
			carry = (acc + carry) - b.targetSegNS
			if carry > b.framePeriodNS {
				carry = b.framePeriodNS
			}
			startSample = i
			acc = 0
			idx++
		}
	}
	// Trailing segment covers the remaining samples.
	segs = append(segs, Segment{
		Index: idx, StartNS: samples[startSample].MediaNS, DurationNS: acc,
		StartSample: startSample, EndSample: len(samples),
		Switchable: b.switchableAt(points, samples[startSample].MediaNS),
	})
	for i := range segs {
		if !segs[i].Switchable {
			segs[i].Rejected = true
		}
	}
	if len(segs) > clock.MaxManifestSegs {
		return nil, clock.NewError(clock.ErrOverflow, "segment count exceeds MaxManifestSegs")
	}
	return segs, nil
}

// switchableAt reports whether an available aligned switch point exists at or
// within one frame period of the given media time.
func (b *Builder) switchableAt(points []fanout.SwitchPoint, mediaNS int64) bool {
	for _, p := range points {
		if !p.Available {
			continue
		}
		diff := p.MediaNS - mediaNS
		if diff < 0 {
			diff = -diff
		}
		if diff <= b.framePeriodNS {
			return true
		}
	}
	return false
}

// measureError computes the largest absolute cumulative timing discrepancy
// between rendition segment boundaries, expressed in nanoseconds.
func (b *Builder) measureError(m *Manifest) int64 {
	if len(m.Renditions) < 2 {
		return 0
	}
	ref := m.Renditions[0].Segments
	var worst int64
	for _, rm := range m.Renditions[1:] {
		n := len(ref)
		if len(rm.Segments) < n {
			n = len(rm.Segments)
		}
		for i := 0; i < n; i++ {
			diff := ref[i].StartNS - rm.Segments[i].StartNS
			if diff < 0 {
				diff = -diff
			}
			if diff > worst {
				worst = diff
			}
		}
	}
	return worst
}

// MaxErrorMS exposes the alignment error in milliseconds for reporting.
func (m *Manifest) MaxErrorMS() float64 { return float64(m.MaxErrorNS) / 1e6 }
