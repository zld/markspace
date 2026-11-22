// Package segment aligns segment boundaries across representations and emits
// verified manifests.
package segment

import (
	"fmt"
	"sort"

	"mediapipeline/internal/container"
	"mediapipeline/internal/media"
)

// Target is one representation's candidate boundary source: a video track and
// an optional audio track that must be cut at the same instants.
type Target struct {
	// Name identifies the representation, e.g. "400kbps".
	Name string
	// Video supplies keyframes; every boundary must be on a keyframe.
	Video *container.Track
	// Audio supplies its sample starts. Audio frames are short, so the
	// aligner snaps to the nearest audio sample start at or before the cut.
	Audio *container.Track
}

// Config controls slicing.
type Config struct {
	// TargetDuration is the desired segment duration.
	TargetDuration media.Nano
	// MaxSegments bounds the number of segments produced.
	MaxSegments int
	// MaxStretchFactor bounds how many target durations one segment may span
	// while the aligner waits for the next common keyframe. Zero means 3.
	MaxStretchFactor int
}

// stretchLimit returns the maximum permitted segment length.
func (c Config) stretchLimit() (media.Nano, error) {
	factor := c.MaxStretchFactor
	if factor == 0 {
		factor = 3
	}
	if factor < 1 {
		return 0, media.NewError(media.InvalidInput, "max stretch factor must be at least 1")
	}
	return mulNonNeg(c.TargetDuration, factor)
}

// Boundary is one aligned cut point.
type Boundary struct {
	Start    media.Nano
	End      media.Nano
	VideoPTS media.Nano
	AudioPTS media.Nano
	HasAudio bool
}

// Plan is the aligned output: one shared boundary list per representation.
type Plan struct {
	Duration   media.Nano
	Boundaries []Boundary
	// PerTarget lists, for each representation, the sample indexes at each
	// boundary so downstream muxing knows exactly which samples belong where.
	PerTarget []TargetPlan
}

// TargetPlan maps the shared boundaries onto one representation's samples.
type TargetPlan struct {
	Name         string
	VideoStarts  []int
	AudioStarts  []int
	SegmentCount int
}

// Align computes segment boundaries shared by every target.
//
// Algorithm: intersect the keyframe instants of all video tracks to obtain
// the set of common keyframes. For each target cut time k*TargetDuration the
// shared boundary is the latest common keyframe not later than the target
// point. A cut can therefore start decoding in every representation at the
// same instant. When no common keyframe exists at or before a target point,
// alignment fails with a located error rather than silently diverging.
func Align(targets []Target, cfg Config) (*Plan, error) {
	if len(targets) == 0 {
		return nil, media.NewError(media.InvalidInput, "no representations to align")
	}
	if cfg.TargetDuration <= 0 {
		return nil, media.NewError(media.InvalidInput, "target duration must be positive")
	}
	if cfg.MaxSegments <= 0 {
		return nil, media.NewError(media.InvalidInput, "max segments must be positive")
	}
	for _, tgt := range targets {
		if tgt.Video == nil {
			return nil, media.NewError(media.InvalidInput, "representation %q has no video track", tgt.Name)
		}
		if len(tgt.Video.Keyframes) == 0 {
			return nil, media.NewError(media.BoundaryNotFound,
				"representation %q has no keyframes", tgt.Name).AtTrack(int32(tgt.Video.ID))
		}
	}

	// Overall content duration is the shortest representation: cutting beyond
	// it would emit segments a representation cannot fill.
	duration := timelineEnd(targets[0].Video)
	for _, tgt := range targets[1:] {
		if d := timelineEnd(tgt.Video); d < duration {
			duration = d
		}
	}
	if duration <= 0 {
		return nil, media.NewError(media.InvalidInput, "content duration is zero")
	}

	common, err := commonKeyframes(targets)
	if err != nil {
		return nil, err
	}
	limit, err := cfg.stretchLimit()
	if err != nil {
		return nil, err
	}

	plan := &Plan{Duration: duration}
	plan.PerTarget = make([]TargetPlan, len(targets))
	for i, tgt := range targets {
		plan.PerTarget[i].Name = tgt.Name
	}

	// The first segment starts at the earliest instant every representation
	// can begin decoding, which is the first common keyframe.
	origin := common[0]
	if origin >= duration {
		return nil, media.NewError(media.BoundaryNotFound,
			"first common keyframe at %d ns is at or after content end %d ns",
			int64(origin), int64(duration)).AtOffset(int64(origin))
	}
	first, err := plan.makeBoundary(origin, targets)
	if err != nil {
		return nil, err
	}
	plan.Boundaries = append(plan.Boundaries, first)
	prev := origin

	for k := 1; ; k++ {
		offset, err := mulNonNeg(cfg.TargetDuration, k)
		if err != nil {
			return nil, err
		}
		want := origin + offset
		if want >= duration {
			break
		}
		if len(plan.Boundaries) >= cfg.MaxSegments {
			return nil, media.NewError(media.InvalidInput,
				"segment count exceeded limit %d", cfg.MaxSegments)
		}
		boundary, ok := LatestAtOrBefore(common, want)
		if !ok {
			return nil, media.NewError(media.BoundaryNotFound,
				"no common keyframe at or before %d ns for representations %s",
				int64(want), targetNames(targets)).AtOffset(int64(want))
		}
		if boundary <= prev {
			// The nearest common keyframe is the one already used: this
			// target point cannot be a cut, so the segment stretches to the
			// next target point rather than being emitted empty. Past a bound
			// the stretch is no longer a reasonable segment and is a hard
			// error, never a silent merge.
			if want-prev > limit {
				return nil, media.NewError(media.BoundaryNotFound,
					"no common keyframe for %d ns after %d ns (stretch limit %d ns); representations %s",
					int64(want-prev), int64(prev), int64(limit), targetNames(targets)).
					AtOffset(int64(prev))
			}
			continue
		}
		b, err := plan.makeBoundary(boundary, targets)
		if err != nil {
			return nil, err
		}
		plan.Boundaries = append(plan.Boundaries, b)
		prev = boundary
	}
	if err := plan.assignSamples(targets); err != nil {
		return nil, err
	}
	return plan, nil
}

// commonKeyframes returns the ascending instants that are keyframes in every
// representation. Keyframe times are compared exactly: a boundary is only
// shareable when every representation can start decoding there.
func commonKeyframes(targets []Target) ([]media.Nano, error) {
	common := append([]media.Nano(nil), targets[0].Video.KeyframeTimes()...)
	if len(common) == 0 {
		return nil, media.NewError(media.BoundaryNotFound,
			"representation %q has no keyframes", targets[0].Name).
			AtTrack(int32(targets[0].Video.ID))
	}
	for _, tgt := range targets[1:] {
		other := tgt.Video.KeyframeTimes()
		intersection := common[:0]
		i, j := 0, 0
		for i < len(common) && j < len(other) {
			switch {
			case common[i] == other[j]:
				intersection = append(intersection, common[i])
				i++
				j++
			case common[i] < other[j]:
				i++
			default:
				j++
			}
		}
		common = intersection
		if len(common) == 0 {
			return nil, media.NewError(media.BoundaryNotFound,
				"representations %s have no keyframe instants in common", targetNames(targets)).
				AtTrack(int32(tgt.Video.ID))
		}
	}
	return common, nil
}

func targetNames(targets []Target) string {
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, t.Name)
	}
	return fmt.Sprint(names)
}

func (p *Plan) makeBoundary(start media.Nano, targets []Target) (Boundary, error) {
	b := Boundary{Start: start, VideoPTS: start}
	for _, tgt := range targets {
		if tgt.Audio == nil {
			continue
		}
		audioStart, ok := LatestAudioAtOrBefore(tgt.Audio, start)
		if !ok {
			return Boundary{}, media.NewError(media.BoundaryNotFound,
				"representation %q has no audio sample at or before %d ns",
				tgt.Name, int64(start)).AtTrack(int32(tgt.Audio.ID))
		}
		// Audio frame durations differ per representation, so the audio start
		// need not be identical; record the latest (worst-case sync offset).
		if audioStart > b.AudioPTS {
			b.AudioPTS = audioStart
		}
		b.HasAudio = true
	}
	return b, nil
}

func (p *Plan) assignSamples(targets []Target) error {
	for i, tgt := range targets {
		plan := &p.PerTarget[i]
		videoStarts, ok := sampleStartsAt(tgt.Video, p.Boundaries)
		if !ok {
			return media.NewError(media.BoundaryNotFound,
				"representation %q video has no sample starting exactly at a boundary",
				tgt.Name).AtTrack(int32(tgt.Video.ID))
		}
		plan.VideoStarts = videoStarts
		plan.SegmentCount = len(plan.VideoStarts)
		if tgt.Audio != nil {
			plan.AudioStarts = audioStartsAt(tgt.Audio, p.Boundaries)
		}
	}
	return nil
}

// sampleStartsAt returns, for each boundary, the index of the sample starting
// exactly at the boundary. Video samples may not be split, so a boundary that
// falls inside a sample is a hard alignment error.
func sampleStartsAt(tr *container.Track, bounds []Boundary) ([]int, bool) {
	out := make([]int, 0, len(bounds))
	j := 0
	for _, b := range bounds {
		for j < len(tr.Samples) && tr.Samples[j].DTSNanos < b.Start {
			j++
		}
		if j >= len(tr.Samples) || tr.Samples[j].DTSNanos != b.Start {
			return nil, false
		}
		out = append(out, j)
	}
	return out, true
}

// audioStartsAt returns, for each boundary, the index of the audio sample
// covering the boundary: the last sample whose start is at or before it.
// Audio frames may straddle a video boundary; the frame is not split.
func audioStartsAt(tr *container.Track, bounds []Boundary) []int {
	out := make([]int, 0, len(bounds))
	for _, b := range bounds {
		idx := sort.Search(len(tr.Samples), func(i int) bool {
			return tr.Samples[i].DTSNanos >= b.Start
		})
		if idx < len(tr.Samples) && tr.Samples[idx].DTSNanos == b.Start {
			out = append(out, idx)
			continue
		}
		if idx > 0 {
			idx--
		}
		if idx >= len(tr.Samples) {
			idx = len(tr.Samples) - 1
		}
		out = append(out, idx)
	}
	return out
}

// LatestAtOrBefore returns the largest keyframe time <= want, if any.
func LatestAtOrBefore(times []media.Nano, want media.Nano) (media.Nano, bool) {
	idx := sort.Search(len(times), func(i int) bool { return times[i] > want })
	if idx == 0 {
		return 0, false
	}
	return times[idx-1], true
}

// LatestAudioAtOrBefore returns the start time of the audio sample covering
// want, i.e. the last sample whose start is at or before it.
func LatestAudioAtOrBefore(tr *container.Track, want media.Nano) (media.Nano, bool) {
	best := media.Nano(-1)
	for _, s := range tr.Samples {
		if s.DTSNanos <= want && s.DTSNanos > best {
			best = s.DTSNanos
		}
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}

func timelineEnd(tr *container.Track) media.Nano {
	if len(tr.Samples) == 0 {
		return 0
	}
	last := tr.Samples[len(tr.Samples)-1]
	return last.DTSNanos + last.DeltaNanos
}

// mulNonNeg multiplies a non-negative duration by a positive integer, failing
// on overflow.
func mulNonNeg(d media.Nano, k int) (media.Nano, error) {
	if d < 0 || k < 0 {
		return 0, media.NewError(media.InvalidInput, "negative operands in time scaling")
	}
	const limit = media.Nano(1 << 62)
	if media.Nano(k) > limit/d {
		return 0, media.NewError(media.Overflow, "scaling %d ns by %d overflows", int64(d), k)
	}
	return d * media.Nano(k), nil
}

// CompareBoundaries verifies two plans share identical cut instants. It is the
// cross-stage invariant used by the CLI and checkpoint validation.
func CompareBoundaries(a, b *Plan) error {
	if len(a.Boundaries) != len(b.Boundaries) {
		return media.NewError(media.InvariantViolation,
			"representation %q has %d boundaries, %q has %d",
			a.PerTarget[0].Name, len(a.Boundaries), b.PerTarget[0].Name, len(b.Boundaries))
	}
	for i := range a.Boundaries {
		if a.Boundaries[i].Start != b.Boundaries[i].Start {
			return media.NewError(media.BoundaryNotFound,
				"boundary %d differs: %d vs %d", i,
				int64(a.Boundaries[i].Start), int64(b.Boundaries[i].Start))
		}
	}
	return nil
}

// Summary renders a one-line human description.
func (p *Plan) Summary() string {
	if len(p.Boundaries) == 0 {
		return "no boundaries"
	}
	first := p.Boundaries[0].Start
	last := p.Boundaries[len(p.Boundaries)-1].Start
	return fmt.Sprintf("%d boundaries from %d ms to %d ms",
		len(p.Boundaries), int64(first)/1e6, int64(last)/1e6)
}
