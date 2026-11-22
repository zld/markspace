// Package manifest turns an alignment result into the reusable manifest
// model. Both the MPD writer and the text report consume this one model,
// so packaging output and diagnostics can never interpret the timeline
// differently.
package manifest

import (
	"dashcheck/internal/align"
	"dashcheck/internal/model"
)

// Build constructs the full model from an alignment result. The timeline
// of every representation is expanded entry by entry using the original
// segment numbers and ticks; nothing is inferred from segment counts.
func Build(contentID string, res *align.Result) (*model.Manifest, error) {
	if len(res.Renditions) == 0 {
		return nil, model.Errorf(model.CategoryAlignment, model.CodeNoSwitchPoint,
			"cannot build manifest without representations")
	}
	longest := longestDuration(res.Renditions)

	reps := make([]*model.Representation, 0, len(res.Renditions))
	for _, ren := range res.Renditions {
		rep, err := buildRepresentation(ren, longest)
		if err != nil {
			return nil, err
		}
		reps = append(reps, rep)
	}

	period := &model.Period{
		ID: 0, Start: model.ZeroRat, Duration: longest,
		AdaptationSets: []*model.AdaptationSet{{
			ID: 0, ContentType: "video", MimeType: "video/mp4",
			Representations: reps,
		}},
	}

	return &model.Manifest{
		ContentID:                 contentID,
		MediaPresentationDuration: longest,
		Period:                    period,
		SwitchPoints:              res.SwitchPoints,
		Rejections:                res.Rejections,
		MaxBoundarySkewMs:         res.MaxSkewMs,
		Tolerance:                 res.Tolerance,
		Loose:                     res.Loose,
		Alignable:                 res.Alignable,
	}, nil
}

func buildRepresentation(ren *align.Rendition, longest model.Rat) (*model.Representation, error) {
	in := ren.Rep
	timeline, usesTime, err := buildTimeline(in.Segments)
	if err != nil {
		return nil, err
	}
	coverage, err := sumDurations(in.Segments)
	if err != nil {
		return nil, err
	}
	if coverage != ren.CoverageTick {
		return nil, model.Errorf(model.CategoryAlignment, model.CodeOverflow,
			"representation %q: timeline coverage %d disagrees with normalized coverage %d",
			in.ID, coverage, ren.CoverageTick)
	}
	media := in.ID + "-$Number$.m4s"
	if usesTime {
		media = in.ID + "-$Time$.m4s"
	}
	delta, err := longest.Sub(ren.Duration)
	if err != nil {
		return nil, err
	}
	deltaMs, err := delta.Mul(model.IntRat(1000))
	if err != nil {
		return nil, err
	}
	return &model.Representation{
		ID: in.ID, Bandwidth: in.Bandwidth, Width: in.Width, Height: in.Height,
		Timescale: in.Timescale,
		Template: model.SegmentTemplate{
			Timescale: in.Timescale, StartNumber: in.Segments[0].Number,
			Media: media, Initialization: in.ID + "-init.mp4",
			Timeline: timeline, UsesTime: usesTime, CoverageTick: coverage,
		},
		Start: ren.Start, End: ren.End, Duration: ren.Duration,
		DeltaToLongestMs: deltaMs,
	}, nil
}

// buildTimeline emits one SegmentTimeline entry per input segment. The
// first entry and every entry following a gap carry an explicit T;
// contiguous entries rely on DASH accumulation, exactly as the spec
// defines SegmentTimeline.
func buildTimeline(segs []model.SegmentRef) ([]model.SegmentTimelineEntry, bool, error) {
	out := make([]model.SegmentTimelineEntry, 0, len(segs))
	contiguous := true
	var prevEnd int64
	for i, s := range segs {
		entry := model.SegmentTimelineEntry{Number: s.Number, T: s.StartTick, D: s.Duration}
		if i == 0 || s.StartTick != prevEnd {
			entry.HasT = true
		}
		if i > 0 && s.Number != segs[i-1].Number+1 {
			contiguous = false
		}
		end, ok := addChecked(s.StartTick, s.Duration)
		if !ok {
			return nil, false, model.Errorf(model.CategoryAlignment, model.CodeOverflow,
				"segment %d: start+duration overflows int64", s.Number)
		}
		prevEnd = end
		out = append(out, entry)
	}
	return out, !contiguous, nil
}

func sumDurations(segs []model.SegmentRef) (int64, error) {
	var total int64
	for _, s := range segs {
		var ok bool
		total, ok = addChecked(total, s.Duration)
		if !ok {
			return 0, model.Errorf(model.CategoryAlignment, model.CodeOverflow,
				"segment %d: accumulated duration overflows int64", s.Number)
		}
	}
	return total, nil
}

func longestDuration(rens []*align.Rendition) model.Rat {
	longest := rens[0].Duration
	for _, ren := range rens[1:] {
		if c, _ := ren.Duration.Compare(longest); c > 0 {
			longest = ren.Duration
		}
	}
	return longest
}

func addChecked(a, b int64) (int64, bool) {
	sum := uint64(a) + uint64(b)
	signed := int64(sum)
	if (a >= 0 && b >= 0 && signed < 0) || (a < 0 && b < 0 && signed >= 0) {
		return 0, false
	}
	return signed, true
}
