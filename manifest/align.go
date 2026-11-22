package manifest

import (
	"fmt"

	"mediadist/timeline"
)

// BoundaryIssue describes one rejected cross-representation boundary.
type BoundaryIssue struct {
	Epoch    int
	Index    int
	RepA     string
	RepB     string
	Field    string
	DeviationMs float64
	LimitMs  float64
}

func (b BoundaryIssue) String() string {
	return fmt.Sprintf("epoch=%d segment=%d reps=%s/%s field=%s deviation_ms=%.3f limit_ms=%.3f",
		b.Epoch, b.Index, b.RepA, b.RepB, b.Field, b.DeviationMs, b.LimitMs)
}

// BoundaryReport is the verifiable alignment result attached to the
// published fragment list.
type BoundaryReport struct {
	ThresholdMs float64
	Segments    int
	MaxDevMs    float64
	Issues      []BoundaryIssue
}

// Aligner checks that representations with different timescales still
// share fragment start/end boundaries within a fixed tolerance.
type Aligner struct {
	thresholdMs float64
}

// NewAligner builds an aligner. The threshold is given in milliseconds
// and must be non-negative.
func NewAligner(thresholdMs float64) (*Aligner, error) {
	if thresholdMs < 0 {
		return nil, timeline.NewError("INVALID_CONFIG", "alignment threshold must be >= 0",
			map[string]any{"threshold_ms": thresholdMs})
	}
	return &Aligner{thresholdMs: thresholdMs}, nil
}

type segKey struct {
	epoch int
	index int
}

// Align compares every matching (epoch, segment index) boundary across
// representations. It returns ALIGNMENT_REJECTED with the offending
// epoch, representations and segment indexes as soon as one boundary
// deviates beyond the threshold; publication must not proceed.
func (a *Aligner) Align(reps []*Representation) (*BoundaryReport, error) {
	if len(reps) < 2 {
		return &BoundaryReport{ThresholdMs: a.thresholdMs}, nil
	}
	grid := map[segKey]map[string]Segment{}
	counts := map[string]int{}
	for _, rep := range reps {
		counts[rep.ID] = len(rep.Segments)
		for _, seg := range rep.Segments {
			k := segKey{seg.Epoch, seg.Index}
			if grid[k] == nil {
				grid[k] = map[string]Segment{}
			}
			grid[k][rep.ID] = seg
		}
	}
	base := reps[0]
	report := &BoundaryReport{ThresholdMs: a.thresholdMs}
	for _, seg := range base.Segments {
		k := segKey{seg.Epoch, seg.Index}
		peers := grid[k]
		for _, other := range reps[1:] {
			opeer, ok := peers[other.ID]
			if !ok {
				issue := BoundaryIssue{
					Epoch: seg.Epoch, Index: seg.Index,
					RepA: base.ID, RepB: other.ID, Field: "missing",
					LimitMs: a.thresholdMs,
				}
				return nil, rejectIssue(issue, counts)
			}
			endA, err := seg.End()
			if err != nil {
				return nil, err
			}
			endB, err := opeer.End()
			if err != nil {
				return nil, err
			}
			if issue, bad := a.diff(seg, opeer, base.ID, other.ID, "start", seg.Start, opeer.Start); bad {
				return nil, rejectIssue(issue, counts)
			} else if issue.DeviationMs > report.MaxDevMs {
				report.MaxDevMs = issue.DeviationMs
			}
			if issue, bad := a.diff(seg, opeer, base.ID, other.ID, "end", endA, endB); bad {
				return nil, rejectIssue(issue, counts)
			} else if issue.DeviationMs > report.MaxDevMs {
				report.MaxDevMs = issue.DeviationMs
			}
		}
		report.Segments++
	}
	return report, nil
}

func (a *Aligner) diff(s1, s2 Segment, id1, id2, field string, t1, t2 timeline.Rat) (BoundaryIssue, bool) {
	d, err := t1.Sub(t2)
	if err != nil {
		return BoundaryIssue{Epoch: s1.Epoch, Index: s1.Index,
			RepA: id1, RepB: id2, Field: field, LimitMs: a.thresholdMs}, true
	}
	dev := d.Float64()
	if dev < 0 {
		dev = -dev
	}
	issue := BoundaryIssue{
		Epoch: s1.Epoch, Index: s1.Index, RepA: id1, RepB: id2,
		Field: field, DeviationMs: dev, LimitMs: a.thresholdMs,
	}
	return issue, dev > a.thresholdMs
}

func rejectIssue(issue BoundaryIssue, counts map[string]int) error {
	return timeline.NewError("ALIGNMENT_REJECTED",
		"cross-representation boundary exceeds tolerance",
		map[string]any{
			"epoch": issue.Epoch, "segment": issue.Index,
			"rep_a": issue.RepA, "rep_b": issue.RepB, "field": issue.Field,
			"deviation_ms": issue.DeviationMs, "limit_ms": issue.LimitMs,
			"rep_counts":   fmt.Sprint(counts),
		})
}

// AlignPlaylist aligns HLS variants (each carrying discontinuity epochs)
// using the same tolerance rule as Align.
func (a *Aligner) AlignPlaylist(variants []*HLSVariant) (*BoundaryReport, error) {
	reps := make([]*Representation, 0, len(variants))
	for i, v := range variants {
		reps = append(reps, &Representation{
			ID:       fmt.Sprintf("hls-%d-%s", i, v.URI),
			Segments: v.Segments,
		})
	}
	return a.Align(reps)
}
