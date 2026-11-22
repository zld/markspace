package align

import (
	"fmt"
	"sort"

	"dashcheck/internal/model"
)

// Align runs normalization and boundary matching. A Result is always
// returned, even on failure, so loose-mode diagnostics and strict-mode
// error messages consume the same computed evidence.
func Align(d *model.Description, tolerance model.Rat, loose bool) (*Result, error) {
	rens, err := Normalize(d)
	if err != nil {
		return nil, err
	}
	res := &Result{
		Renditions: rens, Tolerance: tolerance, Loose: loose,
		MaxSkewMs: model.ZeroRat, Alignable: true,
	}

	all := make([]*Boundary, 0)
	for _, ren := range rens {
		for _, b := range ren.Boundaries {
			if b.Tail {
				// Tail starts never gate publishing; their ranges are
				// carried per-representation in the SegmentTimeline.
				b.consumed = true
				continue
			}
			all = append(all, b)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		c, _ := all[i].Time.Compare(all[j].Time)
		if c != 0 {
			return c < 0
		}
		return all[i].RepID < all[j].RepID
	})

	for _, anchor := range all {
		if anchor.consumed {
			continue
		}
		window := pickWindow(all, anchor, tolerance)
		chosen := choosePerRendition(window, anchor)
		anchor.consumed = true
		if len(chosen) < len(rens) {
			// Consume every boundary in the failed window so the
			// counterpart rendition cannot re-anchor the same skew and
			// emit a duplicate rejection.
			for _, b := range window {
				b.consumed = true
			}
			res.rejectMissing(rens, anchor, chosen, tolerance)
			continue
		}
		for _, b := range chosen {
			if !b.RA {
				res.Alignable = false
				res.Rejections = append(res.Rejections, model.Rejection{
					Reason:         model.CodeBoundaryNotRA,
					Representation: b.RepID, Tick: b.Tick, Time: b.Time,
					SkewMs: skewMs(chosen),
					Detail: fmt.Sprintf("boundary at %s is not a random access point",
						b.Time.DecimalString(6)),
				})
			}
		}
		if !allRA(chosen) {
			continue
		}
		sp := model.SwitchPoint{
			Time:         anchor.Time,
			BoundaryTick: map[string]int64{},
			SkewMs:       skewMs(chosen),
		}
		for _, b := range chosen {
			sp.BoundaryTick[b.RepID] = b.Tick
			b.consumed = true
		}
		res.SwitchPoints = append(res.SwitchPoints, sp)
		if c, _ := sp.SkewMs.Compare(res.MaxSkewMs); c > 0 {
			res.MaxSkewMs = sp.SkewMs
		}
	}

	if len(res.SwitchPoints) == 0 {
		res.Alignable = false
		res.AlignErr = model.Errorf(model.CategoryAlignment, model.CodeNoSwitchPoint,
			"no boundary is simultaneously within tolerance %s and random access on every representation",
			tolerance.String())
	} else if !res.Alignable {
		res.AlignErr = model.Errorf(model.CategoryAlignment, model.CodeToleranceExceeded,
			"%d tolerance and/or random-access failures across the boundaries",
			len(res.Rejections))
	}
	if !res.Alignable {
		return res, res.AlignErr
	}
	return res, nil
}
