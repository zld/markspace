package align

import (
	"fmt"

	"dashcheck/internal/model"
)

func pickWindow(all []*Boundary, anchor *Boundary, tol model.Rat) []*Boundary {
	var win []*Boundary
	for _, b := range all {
		if b.consumed {
			continue
		}
		diff, err := b.Time.Sub(anchor.Time)
		if err != nil {
			continue
		}
		c, err := diff.Compare(model.ZeroRat)
		if err != nil {
			continue
		}
		if c >= 0 && within(diff, tol) {
			win = append(win, b)
		}
	}
	return win
}

func choosePerRendition(window []*Boundary, anchor *Boundary) map[string]*Boundary {
	chosen := map[string]*Boundary{}
	for _, b := range window {
		cur, ok := chosen[b.RepID]
		if !ok {
			chosen[b.RepID] = b
			continue
		}
		if da, errA := absDiff(b.Time, anchor.Time); errA == nil {
			if dc, errC := absDiff(cur.Time, anchor.Time); errC == nil {
				if c, _ := da.Compare(dc); c < 0 {
					chosen[b.RepID] = b
				}
			}
		}
	}
	return chosen
}

func (res *Result) rejectMissing(rens []*Rendition, anchor *Boundary,
	chosen map[string]*Boundary, tol model.Rat) {
	for _, ren := range rens {
		if _, ok := chosen[ren.Rep.ID]; ok {
			continue
		}
		nearest := nearestBoundary(ren.Boundaries, anchor.Time)
		skew, _ := absDiff(nearest.Time, anchor.Time)
		skewMillis, _ := skew.Mul(model.IntRat(1000))
		res.Alignable = false
		res.Rejections = append(res.Rejections, model.Rejection{
			Reason: model.CodeToleranceExceeded, Representation: ren.Rep.ID,
			Tick: nearest.Tick, Time: nearest.Time, SkewMs: skewMillis,
			Detail: fmt.Sprintf(
				"nearest boundary tick=%d at %s deviates %s ms from anchor %s (tolerance %s s)",
				nearest.Tick, nearest.Time.DecimalString(6),
				skewMillis.DecimalString(6), anchor.Time.DecimalString(6), tol.String()),
		})
	}
}

func nearestBoundary(bs []*Boundary, t model.Rat) *Boundary {
	best := bs[0]
	bestDiff, _ := absDiff(best.Time, t)
	for _, b := range bs[1:] {
		d, _ := absDiff(b.Time, t)
		if c, _ := d.Compare(bestDiff); c < 0 {
			best, bestDiff = b, d
		}
	}
	return best
}

func within(diff, tol model.Rat) bool {
	c, _ := diff.Compare(tol)
	return c <= 0
}

func allRA(bs map[string]*Boundary) bool {
	for _, b := range bs {
		if !b.RA {
			return false
		}
	}
	return true
}

func skewMs(bs map[string]*Boundary) model.Rat {
	var minT, maxT model.Rat
	first := true
	for _, b := range bs {
		if first {
			minT, maxT, first = b.Time, b.Time, false
			continue
		}
		if c, _ := b.Time.Compare(minT); c < 0 {
			minT = b.Time
		}
		if c, _ := b.Time.Compare(maxT); c > 0 {
			maxT = b.Time
		}
	}
	diff, _ := maxT.Sub(minT)
	ms, _ := diff.Mul(model.IntRat(1000))
	return ms
}

func absDiff(a, b model.Rat) (model.Rat, error) {
	d, err := a.Sub(b)
	if err != nil {
		return model.Rat{}, err
	}
	return d.Abs()
}
