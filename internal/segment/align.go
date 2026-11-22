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

	plan := &Plan{Duration: duration}
	plan.PerTarget = make([]TargetPlan, len(targets))
	for i, tgt := range targets {
		plan.PerTarget[i].Name = tgt.Name
	}

	var prev media.Nano
	for k := 1; ; k++ {
		want, err := mulNonNeg(cfg.TargetDuration, k)
		if err != nil {
			return nil, err
		}
		if want >= duration {
			break
		}
		if len(plan.Boundaries) >= cfg.MaxSegments {
			return nil, media.NewError(media.InvalidInput,
				"segment count exceeded limit %d", cfg.MaxSegments)
		}
		boundary, ok := LatestAtOrBefore(common, want)
		if !ok || boundary <= prev {
			return nil, media.NewError(media.BoundaryNotFound,
				"no common keyframe in (%d ns, %d ns]: representations %s do not share a cut point",
				int64(prev), int64(want), targetNames(targets)).AtOffset(int64(want))
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
		if !b.HasAudio {
			b.AudioPTS = audioStart
			b.HasAudio = true
		} else if audioStart != b.AudioPTS {
			return Boundary{}, media.NewError(media.BoundaryNotFound,
				"audio tracks disagree at %d ns: %d vs %d",
				int64(start), int64(b.AudioPTS), int64(audioStart))
		}
	}
	return b, nil
}

func (p *Plan) assignSamples(targets []Target) error {
	for i, tgt := range targets {
		plan := &p.PerTarget[i]
		plan.VideoStarts = sampleStartsAt(tgt.Video, p.Boundaries)
		plan.SegmentCount = len(plan.VideoStarts)
		if tgt.Audio != nil {
			plan.AudioStarts = sampleStartsAt(tgt.Audio, p.Boundaries)
			if len(plan.AudioStarts) != len(plan.VideoStarts) {
				return media.NewError(media.BoundaryNotFound,
					"representation %q maps %d audio starts but %d video starts",
					tgt.Name, len(plan.AudioStarts), len(plan.VideoStarts))
			}
		}
	}
	return nil
}

// sampleStartsAt returns, for each boundary, the index of the first sample at
// or after the boundary. A boundary that falls inside a sample is a hard error
// because it would split a decoded unit.
func sampleStartsAt(tr *container.Track, bounds []Boundary) []int {
	out := make([]int, 0, len(bounds))
	j := 0
	for _, b := range bounds {
		for j < len(tr.Samples) && tr.Samples[j].DTSNanos < b.Start {
			j++
		}
		if j < len(tr.Samples) && tr.Samples[j].DTSNanos > b.Start {
			// Boundary does not coincide with a sample start.
			return nil
		}
		out = append(out, j)
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

// LatestAudioAtOrBefore returns the presentation time of the audio sample
// covering want, i.e. the last sample whose PTS is <= want.
func LatestAudioAtOrBefore(tr *container.Track, want media.Nano) (media.Nano, bool) {
	best := media.Nano(-1)
	for _, s := range tr.Samples {
		if s.PTSNanos <= want && s.PTSNanos > best {
			best = s.PTSNanos
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
