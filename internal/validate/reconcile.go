package validate

import (
	"errors"
	"fmt"

	"dashgen/internal/align"
	"dashgen/internal/manifest"
	"dashgen/internal/mpd"
	"dashgen/internal/plan"
	"dashgen/internal/timebase"
)

// Verification is the full result of comparing a generated MPD back against
// its source plan and applying the alignment tolerances.
type Verification struct {
	RepresentationCount  int
	ExpandedSegmentCount int64
	MaxStartOffset       timebase.Rational
	MaxDurationDelta     timebase.Rational
	TemplateExample      string
	ReconcileFailures    []string
	Tolerance            *Result
}

// Failed reports any reconciliation mismatch or tolerance violation.
func (v *Verification) Failed() bool {
	return len(v.ReconcileFailures) > 0 || (v.Tolerance != nil && v.Tolerance.Failed())
}

// Reconcile parses nothing itself: callers supply the decoded MPD and plan.
// It checks representation identity, timescales, start numbers and every
// expanded segment's native ticks, then applies tolerances across renditions.
// Structural timeline defects reported by the aligner are surfaced as
// reconciliation failures. Only nil inputs return a Go error.
func Reconcile(doc *manifest.MPD, p *plan.Plan, tol Tolerance) (*Verification, error) {
	if doc == nil {
		return nil, fmt.Errorf("validate: nil MPD")
	}
	if p == nil {
		return nil, fmt.Errorf("validate: nil plan")
	}
	report, alignErr := align.Build(p)
	v := &Verification{}
	if alignErr != nil {
		for _, e := range splitJoin(alignErr) {
			v.ReconcileFailures = append(v.ReconcileFailures, e.Error())
		}
	}

	docReps := flattenRepresentations(doc)
	v.RepresentationCount = len(docReps)
	planByID := map[string]*plan.Representation{}
	for i := range p.Representations {
		planByID[p.Representations[i].ID] = &p.Representations[i]
	}
	docByID := map[string]*manifest.Representation{}
	for i := range docReps {
		docByID[docReps[i].ID] = &docReps[i]
	}
	for id := range planByID {
		if _, ok := docByID[id]; !ok {
			v.ReconcileFailures = append(v.ReconcileFailures,
				fmt.Sprintf("representation %q present in plan but missing from MPD", id))
		}
	}
	for id := range docByID {
		if _, ok := planByID[id]; !ok {
			v.ReconcileFailures = append(v.ReconcileFailures,
				fmt.Sprintf("representation %q present in MPD but missing from plan", id))
		}
	}

	for i := range p.Representations {
		prep := &p.Representations[i]
		drep, ok := docByID[prep.ID]
		if !ok {
			continue
		}
		reconcileRepresentation(prep, drep, v)
	}

	v.ExpandedSegmentCount = countExpanded(docReps)
	if len(docReps) > 0 {
		first := &docReps[0]
		if first.SegmentTemplate != nil && first.SegmentTemplate.SegmentTimeline != nil {
			ticks, err := manifest.ExpandTimeline(first.SegmentTemplate.SegmentTimeline)
			if err == nil && len(ticks) > 0 {
				path, err := mpd.Expand(first.SegmentTemplate.Media, mpd.SegmentRef{
					RepID: first.ID, Number: first.SegmentTemplate.StartNumber, Time: ticks[0].Start,
				})
				if err != nil {
					v.ReconcileFailures = append(v.ReconcileFailures,
						fmt.Sprintf("representation %q: template example: %v", first.ID, err))
				} else {
					v.TemplateExample = path
				}
			}
		}
	}

	if report != nil {
		v.MaxStartOffset = report.MaxStartOffset
		v.MaxDurationDelta = report.MaxDurationDelta
		tres, err := CheckTolerances(report, tol)
		if err != nil {
			return nil, err
		}
		v.Tolerance = tres
	}
	return v, nil
}

func reconcileRepresentation(prep *plan.Representation, drep *manifest.Representation, v *Verification) {
	st := drep.SegmentTemplate
	if st == nil {
		v.ReconcileFailures = append(v.ReconcileFailures,
			fmt.Sprintf("representation %q: MPD has no SegmentTemplate", prep.ID))
		return
	}
	if st.Timescale != prep.Timescale {
		v.ReconcileFailures = append(v.ReconcileFailures,
			fmt.Sprintf("representation %q: timescale MPD=%d plan=%d", prep.ID, st.Timescale, prep.Timescale))
	}
	wantStart := prep.Segments[0].Number
	if st.StartNumber != wantStart {
		v.ReconcileFailures = append(v.ReconcileFailures,
			fmt.Sprintf("representation %q: startNumber MPD=%d plan=%d", prep.ID, st.StartNumber, wantStart))
	}
	ticks, err := manifest.ExpandTimeline(st.SegmentTimeline)
	if err != nil {
		v.ReconcileFailures = append(v.ReconcileFailures,
			fmt.Sprintf("representation %q: timeline: %v", prep.ID, err))
		return
	}
	if len(ticks) != len(prep.Segments) {
		v.ReconcileFailures = append(v.ReconcileFailures,
			fmt.Sprintf("representation %q: segment count MPD=%d plan=%d",
				prep.ID, len(ticks), len(prep.Segments)))
		return
	}
	for i, seg := range prep.Segments {
		if ticks[i].Start != seg.StartTicks || ticks[i].Duration != seg.DurationTicks {
			v.ReconcileFailures = append(v.ReconcileFailures,
				fmt.Sprintf("representation %q segment %d: MPD ticks (t=%d,d=%d) differ from plan (t=%d,d=%d)",
					prep.ID, seg.Number, ticks[i].Start, ticks[i].Duration, seg.StartTicks, seg.DurationTicks))
		}
	}
}

func flattenRepresentations(doc *manifest.MPD) []manifest.Representation {
	var reps []manifest.Representation
	for _, period := range doc.Periods {
		for _, set := range period.AdaptationSets {
			reps = append(reps, set.Representations...)
		}
	}
	return reps
}

func countExpanded(reps []manifest.Representation) int64 {
	var n int64
	for i := range reps {
		st := reps[i].SegmentTemplate
		if st == nil || st.SegmentTimeline == nil {
			continue
		}
		n += manifest.TimelineSegmentCount(st.SegmentTimeline)
	}
	return n
}

func splitJoin(err error) []error {
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		return joined.Unwrap()
	}
	return []error{err}
}
