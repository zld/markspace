// Package align normalizes per-representation tick timelines onto one
// rational second base and decides which segment boundaries are safe
// bitrate-switch points. Matching is done on normalized time, never on
// segment index, so renditions that started encoding at different
// moments cannot be paired just because they share a segment number.
package align

import (
	"dashcheck/internal/model"
)

// Boundary is one segment start expressed both in the rendition's own
// ticks and in unified seconds. Original ticks are always retained so
// diagnostics can report the unmodified encode timeline.
type Boundary struct {
	RepID  string
	Tick   int64
	Time   model.Rat
	RA     bool
	Number int64
	// Tail marks the start of the final segment. Per the publishing
	// contract, tail segments may legitimately differ between rends and
	// are excluded from common-switch-point matching; their real ranges
	// remain fully expressed in each representation's SegmentTimeline.
	Tail     bool
	consumed bool
}

// Rendition is a fully normalized representation.
type Rendition struct {
	Rep        *model.RepresentationInput
	Boundaries []*Boundary
	Start      model.Rat
	End        model.Rat
	Duration   model.Rat
	// CoverageTick sums every segment duration in native ticks and is
	// re-checked by the MPD writer against the timeline.
	CoverageTick int64
}

// Result carries everything manifest building and reporting need.
type Result struct {
	Renditions   []*Rendition
	SwitchPoints []model.SwitchPoint
	Rejections   []model.Rejection
	MaxSkewMs    model.Rat
	Tolerance    model.Rat
	Loose        bool
	Alignable    bool
	AlignErr     *model.Error
}

// Normalize converts every representation onto the unified second base.
func Normalize(d *model.Description) ([]*Rendition, error) {
	out := make([]*Rendition, 0, len(d.Representations))
	for i := range d.Representations {
		r := &d.Representations[i]
		ren := &Rendition{Rep: r}
		var coverage int64
		for si := range r.Segments {
			s := &r.Segments[si]
			t, err := model.NewRat(s.StartTick, r.Timescale)
			if err != nil {
				return nil, err
			}
			ren.Boundaries = append(ren.Boundaries, &Boundary{
				RepID: r.ID, Tick: s.StartTick, Time: t,
				RA: s.RandomAccess, Number: s.Number,
				Tail: si == len(r.Segments)-1,
			})
			endTick, ok := addChecked(s.StartTick, s.Duration)
			if !ok {
				return nil, model.Errorf(model.CategoryAlignment, model.CodeOverflow,
					"representation %q segment %d: end tick overflows int64",
					r.ID, s.Number)
			}
			end, err := model.NewRat(endTick, r.Timescale)
			if err != nil {
				return nil, err
			}
			if si == 0 {
				ren.Start = t
			}
			ren.End = end
			if coverage, ok = addChecked(coverage, s.Duration); !ok {
				return nil, model.Errorf(model.CategoryAlignment, model.CodeOverflow,
					"representation %q: summed segment durations overflow int64", r.ID)
			}
		}
		dur, err := ren.End.Sub(ren.Start)
		if err != nil {
			return nil, err
		}
		ren.Duration = dur
		ren.CoverageTick = coverage
		out = append(out, ren)
	}
	return out, nil
}

// addChecked returns a+b with signed int64 overflow detection.
func addChecked(a, b int64) (int64, bool) {
	sum := uint64(a) + uint64(b)
	signed := int64(sum)
	if (a >= 0 && b >= 0 && signed < 0) || (a < 0 && b < 0 && signed >= 0) {
		return 0, false
	}
	return signed, true
}
