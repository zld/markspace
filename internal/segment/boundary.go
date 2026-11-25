// Package segment validates fragment boundaries on the shared clock: adjacent
// segments of one representation must be contiguous (no gaps, no overlaps)
// and every cut point that starts a new fragment on the video track must sit
// on a keyframe. Failures are returned as located MediaErrors while the input
// descriptors are preserved, so callers can cut the timeline at the fault and
// keep processing the remaining representations.
package segment

import (
	"mediasync/internal/model"
)

// BoundaryReport collects the validation outcome for one representation.
type BoundaryReport struct {
	Representation string
	Checked        int
	Errors         []*model.MediaError
}

// ValidateOrdering checks contiguity of a single representation's segments in
// the given order. A gap yields KindBoundaryGap, an overlap
// KindBoundaryOverlap; both carry the offending segment's index as offset.
// Segments belonging to different periods are never compared, so an HLS
// discontinuity or DASH period transition never looks like a gap.
func ValidateOrdering(segs []model.SegmentDescriptor) BoundaryReport {
	rep := BoundaryReport{Representation: repName(segs)}
	rep.Checked = len(segs)
	if len(segs) < 2 {
		return rep
	}
	for i := 1; i < len(segs); i++ {
		prev, cur := segs[i-1], segs[i]
		if prev.PeriodID != cur.PeriodID {
			continue // period boundary: timelines restart by definition
		}
		switch {
		case cur.StartNS > prev.EndNS():
			rep.Errors = append(rep.Errors, model.NewError(model.KindBoundaryGap,
				"segment", int64(cur.Index),
				"rep %s gap %dns between seg %d and %d (period %d)",
				cur.Representation, cur.StartNS-prev.EndNS(), prev.Index, cur.Index, cur.PeriodID))
		case cur.StartNS < prev.EndNS():
			rep.Errors = append(rep.Errors, model.NewError(model.KindBoundaryOverlap,
				"segment", int64(cur.Index),
				"rep %s overlap %dns at seg %d (period %d)",
				cur.Representation, prev.EndNS()-cur.StartNS, cur.Index, cur.PeriodID))
		}
	}
	return rep
}

// ValidateKeyframes checks that every video segment in a period starts on a
// keyframe. Non-video tracks have no sync-sample requirement and are skipped.
// The first segment of each period is exempt because period entry points are
// established by the manifest rather than an in-timeline cut.
func ValidateKeyframes(segs []model.SegmentDescriptor) BoundaryReport {
	rep := BoundaryReport{Representation: repName(segs)}
	rep.Checked = len(segs)
	periodFirst := map[int]bool{}
	for _, s := range segs {
		if s.Kind != model.KindVideo {
			continue
		}
		if !periodFirst[s.PeriodID] {
			periodFirst[s.PeriodID] = true
			continue
		}
		if !s.Keyframe {
			rep.Errors = append(rep.Errors, model.NewError(model.KindMissingKeyframe,
				"segment", int64(s.Index),
				"rep %s video seg %d period %d is not a keyframe cut",
				s.Representation, s.Index, s.PeriodID))
		}
	}
	return rep
}

// ValidateAll runs ordering and keyframe checks across every representation
// group in the input and returns one report per distinct representation.
func ValidateAll(segs []model.SegmentDescriptor) []BoundaryReport {
	groups := map[string][]model.SegmentDescriptor{}
	var order []string
	for _, s := range segs {
		if _, ok := groups[s.Representation]; !ok {
			order = append(order, s.Representation)
		}
		groups[s.Representation] = append(groups[s.Representation], s)
	}
	reports := make([]BoundaryReport, 0, len(order))
	for _, name := range order {
		group := groups[name]
		merged := BoundaryReport{Representation: name}
		for _, r := range []BoundaryReport{ValidateOrdering(group), ValidateKeyframes(group)} {
			merged.Checked += r.Checked
			merged.Errors = append(merged.Errors, r.Errors...)
		}
		reports = append(reports, merged)
	}
	return reports
}

func repName(segs []model.SegmentDescriptor) string {
	if len(segs) == 0 {
		return ""
	}
	return segs[0].Representation
}
