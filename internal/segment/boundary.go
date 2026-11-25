// Package segment selects decode boundaries and aligns bitrate representations.
package segment

import (
	"fmt"
	"sort"
	"time"

	"rtptrace/internal/manifest"
	"rtptrace/internal/timeline"
)

// BoundaryState labels deterministic boundary selection outcomes.
const (
	BoundaryAligned    = "aligned"
	BoundaryMissingKey = "failed-missing-keyframe"
	BoundaryTruncated  = "failed-truncated-sample-table"
)

// Boundary is one target segment start and its selected decode anchor.
type Boundary struct {
	TrackID               uint32 `json:"track_id"`
	Index                 int    `json:"index"`
	SegmentID             string `json:"segment_id"`
	TargetStartNanos      int64  `json:"target_start_ns"`
	PresentationStart     string `json:"presentation_start"`
	PresentationTicks     int64  `json:"presentation_ticks"`
	PresentationTimescale uint32 `json:"presentation_timescale"`
	DecodeStartSample     uint32 `json:"decode_start_sample"`
	DecodeStartTicks      int64  `json:"decode_start_ticks,omitempty"`
	PrerollSamples        int    `json:"preroll_samples"`
	State                 string `json:"state"`
	FailureSample         uint32 `json:"failure_sample,omitempty"`
	Message               string `json:"message,omitempty"`
}

// TrackBoundaries is the boundary plan for one track.
type TrackBoundaries struct {
	TrackID    uint32     `json:"track_id"`
	Kind       string     `json:"kind"`
	Boundaries []Boundary `json:"boundaries"`
}

// SegmentID renders the stable zero-padded segment identifier.
func SegmentID(index, count int) string {
	width := 3
	if count > 999 {
		width = len(fmt.Sprint(count - 1))
	}
	return fmt.Sprintf("seg-%0*d", width, index)
}

// PresentationTime returns the exact presentation time used for a sample.
func PresentationTime(t manifest.Track, s manifest.Sample) timeline.Rational {
	scale := t.MovieTimescale
	ticks := s.PresentationTicks
	if scale == 0 {
		scale = t.SourceTimescale
		ticks = s.PresentationTicks - t.StartTimeTicks
	}
	return timeline.Rat(ticks, int64(scale))
}

// TargetTime converts a segment index and duration to exact seconds.
func TargetTime(index int, segment time.Duration) timeline.Rational {
	nanos := segment.Nanoseconds() * int64(index)
	return timeline.Rat(nanos, int64(time.Second))
}

// SelectTrackBoundaries chooses the latest sync sample not after each target.
// The target itself is never moved when no usable keyframe exists.
func SelectTrackBoundaries(t manifest.Track, segment time.Duration, count int) TrackBoundaries {
	out := TrackBoundaries{TrackID: t.ID, Kind: string(t.Kind)}
	sorted := append([]manifest.Sample(nil), t.Samples...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return PresentationTime(t, sorted[i]).Cmp(PresentationTime(t, sorted[j])) < 0
	})
	scale := t.MovieTimescale
	if scale == 0 {
		scale = t.SourceTimescale
	}

	for i := 0; i < count; i++ {
		target := TargetTime(i, segment)
		targetTicks := target.Mul(timeline.Rat(int64(scale), 1)).Floor()
		b := Boundary{
			TrackID: t.ID,
			Index:   i, SegmentID: SegmentID(i, count), TargetStartNanos: segment.Nanoseconds() * int64(i),
			PresentationStart: target.String(), PresentationTicks: targetTicks,
			PresentationTimescale: scale, State: BoundaryAligned,
		}

		if t.Kind != manifest.TrackVideo {
			first := sampleAtOrAfter(t, sorted, target)
			if first != nil {
				b.DecodeStartSample = first.Index
				b.DecodeStartTicks = first.DecodeTicks
			}
			out.Boundaries = append(out.Boundaries, b)
			continue
		}

		var anchor *manifest.Sample
		for j := range sorted {
			pt := PresentationTime(t, sorted[j])
			if sorted[j].Sync && pt.Cmp(target) <= 0 {
				s := sorted[j]
				anchor = &s
			}
		}
		if anchor == nil {
			near := sampleAtOrAfter(t, sorted, target)
			b.State = BoundaryMissingKey
			if near != nil {
				b.FailureSample = near.Index
				b.Message = fmt.Sprintf("no sync sample at or before %s; first source sample at boundary is %d", target, near.Index)
			} else if len(t.Samples) > 0 {
				b.FailureSample = t.Samples[len(t.Samples)-1].Index + 1
				b.Message = fmt.Sprintf("no source sample reaches target %s", target)
			} else {
				b.FailureSample = 1
				b.Message = "track has no samples"
			}
		} else {
			b.DecodeStartSample = anchor.Index
			b.DecodeStartTicks = anchor.DecodeTicks
			b.PrerollSamples = countPrerollSamples(t, sorted, *anchor, target)
		}
		out.Boundaries = append(out.Boundaries, b)
	}
	return out
}

func sampleAtOrAfter(t manifest.Track, sorted []manifest.Sample, target timeline.Rational) *manifest.Sample {
	for j := range sorted {
		if PresentationTime(t, sorted[j]).Cmp(target) >= 0 {
			s := sorted[j]
			return &s
		}
	}
	return nil
}

func countPrerollSamples(t manifest.Track, sorted []manifest.Sample, anchor manifest.Sample, target timeline.Rational) int {
	anchorTime := PresentationTime(t, anchor)
	count := 0
	for _, s := range sorted {
		pt := PresentationTime(t, s)
		if pt.Cmp(anchorTime) >= 0 && pt.Cmp(target) < 0 {
			count++
		}
	}
	if count > 0 {
		return count - 1 // anchor itself is not preroll
	}
	return 0
}
