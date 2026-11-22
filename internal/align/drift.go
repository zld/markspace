// Package align estimates clock drift between a media clock and a reference,
// and derives VFR-aware interval geometry from real sample timing.
package align

import (
	"math"
	"sort"

	"mediaarchive/internal/clock"
	"mediaarchive/internal/diag"
)

// Point pairs one observed media timestamp with a reference timestamp, both
// in seconds on the unified base. Reference is the trusted side (e.g. a
// subtitle or wall clock capture), Media is the drifting side.
type Point struct {
	Media float64
	Ref   float64
}

// DriftResult is a least-squares fit ref = a + b*media. PPM is (b-1)*1e6, so
// positive PPM means the media clock runs fast.
type DriftResult struct {
	PPM        float64
	Intercept  float64
	Samples    int
	MaxResidMs float64
	RMSEms     float64
	// Stable reports whether drift was actually estimable from the points.
	Stable bool
}

// EstimateDrift fits a line through at least three well-separated points.
// Fewer points or a degenerate span return a structured CodeMissingBase
// fault: silently claiming zero drift would make the manifest unauditable.
func EstimateDrift(points []Point, srcOffset int64) (DriftResult, error) {
	pts := append([]Point(nil), points...)
	sort.Slice(pts, func(i, j int) bool { return pts[i].Media < pts[j].Media })
	if len(pts) < 3 {
		return DriftResult{}, diag.New(diag.CodeMissingBase, srcOffset,
			"drift estimation needs >=3 anchor points, got %d", len(pts))
	}
	// Reject duplicate media times: they carry no slope information.
	uniq := pts[:0]
	for i, p := range pts {
		if i > 0 && p.Media == pts[i-1].Media {
			continue
		}
		uniq = append(uniq, p)
	}
	pts = uniq
	if len(pts) < 3 {
		return DriftResult{}, diag.New(diag.CodeMissingBase, srcOffset,
			"drift estimation needs 3 distinct media anchors, got %d", len(pts))
	}
	n := float64(len(pts))
	var sx, sy float64
	for _, p := range pts {
		sx += p.Media
		sy += p.Ref
	}
	mx, my := sx/n, sy/n
	var num, den float64
	for _, p := range pts {
		dx := p.Media - mx
		num += dx * (p.Ref - my)
		den += dx * dx
	}
	if den == 0 {
		return DriftResult{}, diag.New(diag.CodeMissingBase, srcOffset, "media anchors span zero time")
	}
	b := num / den
	a := my - b*mx
	res := DriftResult{PPM: (b - 1) * 1e6, Intercept: a, Samples: len(pts), Stable: true}
	var sumSq float64
	for _, p := range pts {
		r := p.Ref - (a + b*p.Media)
		sumSq += r * r
		if v := math.Abs(r) * 1000; v > res.MaxResidMs {
			res.MaxResidMs = v
		}
	}
	res.RMSEms = math.Sqrt(sumSq/n) * 1000
	return res, nil
}

// Correct maps a drifting media second onto the reference clock.
func (d DriftResult) Correct(media float64) float64 { return d.Intercept + (1+d.PPM/1e6)*media }

// CorrectRational applies the fit with exact rational input, rounding once at
// the end so repeated corrections do not accumulate float error.
func (d DriftResult) CorrectRational(r clock.Rational, timescale int64) clock.Rational {
	sec := d.Correct(r.Float())
	return clock.FromTicks(int64(math.Round(sec*float64(timescale))), timescale)
}

// Residuals returns per-point residuals in milliseconds, for tests and audit.
func (d DriftResult) Residuals(points []Point) []float64 {
	out := make([]float64, len(points))
	for i, p := range points {
		out[i] = (p.Ref - d.Correct(p.Media)) * 1000
	}
	return out
}
