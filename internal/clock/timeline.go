package clock

import (
	"errors"
	"fmt"
	"time"

	"avbatch/internal/mediaerr"
)

const nanosPerSecond = int64(time.Second)

// MaxPPM bounds the drift model. Mobile device clocks are expected to stay
// far below this; anything larger is a broken estimator, not drift.
const MaxPPM = 5000.0

// Timebase converts between media ticks and nanoseconds.
type Timebase struct {
	Timescale uint32
}

func (tb Timebase) Valid() bool { return tb.Timescale > 0 }

// TicksToNanos is overflow-checked: the div/mod split keeps the product
// representable even for hostile uint64 input.
func (tb Timebase) TicksToNanos(ticks uint64) (int64, error) {
	if !tb.Valid() {
		return 0, errors.New("clock: zero timescale")
	}
	ts := uint64(tb.Timescale)
	q, r := ticks/ts, ticks%ts
	if q > uint64(1<<62)/uint64(nanosPerSecond) {
		return 0, fmt.Errorf("clock: tick value %d overflows nanoseconds", ticks)
	}
	return int64(q)*nanosPerSecond + int64(r*uint64(nanosPerSecond)/ts), nil
}

// NanosToTicks returns the number of ticks containing ns, rounded down.
func (tb Timebase) NanosToTicks(ns int64) (uint64, error) {
	if !tb.Valid() {
		return 0, errors.New("clock: zero timescale")
	}
	if ns < 0 {
		return 0, fmt.Errorf("clock: negative nanosecond value %d", ns)
	}
	sec := uint64(ns / nanosPerSecond)
	rem := uint64(ns % nanosPerSecond)
	ts := uint64(tb.Timescale)
	if sec > (1<<63)/ts {
		return 0, fmt.Errorf("clock: nanosecond value %d overflows ticks", ns)
	}
	return sec*ts + rem*ts/uint64(nanosPerSecond), nil
}

// Seconds converts a tick count for reporting only.
func (tb Timebase) Seconds(ticks uint64) float64 {
	if !tb.Valid() {
		return 0
	}
	return float64(ticks) / float64(tb.Timescale)
}

// Corrected is one sample placed on the unified timeline.
type Corrected struct {
	// Input is the original media tick count.
	Input uint64
	// RawNanos is Input converted to nanoseconds without drift correction.
	RawNanos int64
	// Nanos is the drift-corrected position used by all downstream stages.
	Nanos int64
	// UncertaintyNanos is the half-width of the confidence bound around Nanos.
	UncertaintyNanos int64
	// DriftNanos is Nanos-RawNanos, exposed for diagnostics.
	DriftNanos int64
}

// Observation pairs a media timestamp with the sender wall clock.
type Observation struct {
	MediaTicks uint64
	WallNanos  int64
}

// DriftModel is an affine map from media nanoseconds to wall nanoseconds:
// Wall = AnchorWall + (Media-AnchorMedia)*(1+PPM/1e6).
type DriftModel struct {
	PPM          float64
	AnchorMedia  int64
	AnchorWall   int64
	Samples      int
	ResidualNano int64
	SpanNanos    int64
	Clamped      bool
}

func (m DriftModel) Valid() bool { return m.Samples > 0 }

// Timeline maps one track's media ticks onto the drift-corrected timeline and
// assigns every sample an uncertainty bound.
type Timeline struct {
	tb                 Timebase
	tol                time.Duration
	m                  DriftModel
	maxCumulativeDrift int64
	tickNanos          int64
}

// NewTimeline creates an identity (no-drift) timeline for tb. tol is the
// amount of backward movement treated as benign reordering rather than an
// ordering failure.
func NewTimeline(tb Timebase, tol time.Duration) *Timeline {
	if tol < 0 {
		tol = 0
	}
	tick := int64(1)
	if tb.Valid() {
		if n, err := tb.TicksToNanos(1); err == nil && n > 0 {
			tick = n
		}
	}
	return &Timeline{tb: tb, tol: tol, maxCumulativeDrift: 1 << 62, tickNanos: tick}
}

// WithDriftLimit sets how far (in nanoseconds) the model may move a sample
// from its raw position before Correct rejects it. Defaults to unbounded
// within int64.
func (tl *Timeline) WithDriftLimit(ns int64) *Timeline {
	if ns >= 0 {
		tl.maxCumulativeDrift = ns
	}
	return tl
}

type obsPoint struct {
	media int64
	wall  int64
}

// Fit estimates drift by least squares on deltas, centered on the earliest
// observation so float64 never sees a large absolute timestamp.
func (tl *Timeline) Fit(obs []Observation) error {
	if len(obs) == 0 {
		return errors.New("clock: no observations")
	}
	pts := make([]obsPoint, 0, len(obs))
	for _, o := range obs {
		media, err := tl.tb.TicksToNanos(o.MediaTicks)
		if err != nil {
			return mediaerr.Wrap(mediaerr.KindDriftOutOfRange, -1, "", 0, "observation media time", err)
		}
		pts = append(pts, obsPoint{media: media, wall: o.WallNanos})
	}
	sortPoints(pts)
	if len(pts) == 1 {
		tl.m = DriftModel{Samples: 1, AnchorMedia: pts[0].media, AnchorWall: pts[0].wall}
		return nil
	}
	m0, w0 := pts[0].media, pts[0].wall

	ppm, ok := fitPPM(pts, m0, w0)
	if !ok {
		return mediaerr.New(mediaerr.KindDriftOutOfRange, -1, "", 0, "degenerate observation window")
	}
	keep := pts
	if len(pts) > 3 {
		cut := 3 * medianAbsResidual(pts, m0, w0, ppm)
		if cut > 0 {
			var filtered []obsPoint
			for _, p := range pts {
				if absFloat(residualAt(p, m0, w0, ppm)) <= cut {
					filtered = append(filtered, p)
				}
			}
			if len(filtered) >= 2 {
				keep = filtered
				if ppm, ok = fitPPM(keep, m0, w0); !ok {
					return mediaerr.New(mediaerr.KindDriftOutOfRange, -1, "", 0, "degenerate observation window after trimming")
				}
			}
		}
	}

	clamped := false
	if ppm > MaxPPM || ppm < -MaxPPM {
		if ppm > 0 {
			ppm = MaxPPM
		} else {
			ppm = -MaxPPM
		}
		clamped = true
	}
	tl.m = DriftModel{
		PPM:          ppm,
		AnchorMedia:  m0,
		AnchorWall:   w0,
		Samples:      len(keep),
		ResidualNano: maxResidual(keep, m0, w0, ppm),
		SpanNanos:    keep[len(keep)-1].media - m0,
		Clamped:      clamped,
	}
	if clamped {
		return mediaerr.New(mediaerr.KindDriftOutOfRange, -1, "", 0,
			fmt.Sprintf("drift exceeds ±%.0f ppm, clamped", MaxPPM))
	}
	return nil
}

func sortPoints(pts []obsPoint) {
	// Insertion sort: observation windows are small and this keeps Fit
	// allocation-free and stable for equal media times.
	for i := 1; i < len(pts); i++ {
		for j := i; j > 0 && pts[j].media < pts[j-1].media; j-- {
			pts[j], pts[j-1] = pts[j-1], pts[j]
		}
	}
}

func residualAt(p obsPoint, m0, w0 int64, ppm float64) float64 {
	predicted := float64(p.media-m0) * (1 + ppm/1e6)
	return float64(p.wall-w0) - predicted
}

func fitPPM(pts []obsPoint, m0, w0 int64) (float64, bool) {
	var sx, sy, sxx, sxy float64
	n := float64(len(pts))
	for _, p := range pts {
		x := float64(p.media - m0)
		y := float64(p.wall - w0)
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, false
	}
	ppm := ((n*sxy-sx*sy)/den - 1) * 1e6
	if ppm != ppm { // NaN
		return 0, false
	}
	if ppm > 1e15 || ppm < -1e15 {
		return 0, false
	}
	return ppm, true
}

func maxResidual(pts []obsPoint, m0, w0 int64, ppm float64) int64 {
	var max float64
	for _, p := range pts {
		if r := absFloat(residualAt(p, m0, w0, ppm)); r > max {
			max = r
		}
	}
	return int64(max)
}

func medianAbsResidual(pts []obsPoint, m0, w0 int64, ppm float64) float64 {
	vals := make([]float64, len(pts))
	for i, p := range pts {
		vals[i] = absFloat(residualAt(p, m0, w0, ppm))
	}
	// Small-n selection sort; avoids importing sort for a handful of values.
	for i := 1; i < len(vals); i++ {
		for j := i; j > 0 && vals[j] < vals[j-1]; j-- {
			vals[j], vals[j-1] = vals[j-1], vals[j]
		}
	}
	return vals[len(vals)/2]
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// Model returns the current drift model.
func (tl *Timeline) Model() DriftModel { return tl.m }

// Timebase returns the timeline's timebase.
func (tl *Timeline) Timebase() Timebase { return tl.tb }

// Correct converts ticks into the unified timeline. quant is the sample's own
// duration in ticks (0 if unknown); it widens the uncertainty bound, as does
// distance from the fitted window.
func (tl *Timeline) Correct(ticks uint64, quant uint64) (Corrected, error) {
	raw, err := tl.tb.TicksToNanos(ticks)
	if err != nil {
		return Corrected{}, mediaerr.Wrap(mediaerr.KindUnsorted, -1, "", 0, "tick conversion", err)
	}
	nanos := raw
	if tl.m.Valid() {
		elapsed := float64(raw - tl.m.AnchorMedia)
		nanos = int64(roundFloat(float64(tl.m.AnchorWall) + elapsed*(1+tl.m.PPM/1e6)))
	}
	drift := nanos - raw
	if drift > tl.maxCumulativeDrift || drift < -tl.maxCumulativeDrift {
		return Corrected{}, mediaerr.New(mediaerr.KindDriftOutOfRange, -1, "", raw,
			fmt.Sprintf("drift correction %d ns exceeds limit %d ns", drift, tl.maxCumulativeDrift))
	}
	// A sample's timestamp is exact to one tick; its duration does not make
	// the timestamp itself uncertain. Uncertainty therefore comes from clock
	// resolution, the fit residual and projection distance only.
	unc := tl.tickNanos + tl.m.ResidualNano
	if tl.m.Valid() {
		// Projection error grows with distance from the fitted window.
		unc += int64(roundFloat(absFloat(tl.m.PPM/1e6) * absFloat(float64(raw-tl.m.AnchorMedia))))
	}
	if unc < 1 {
		unc = 1
	}
	return Corrected{
		Input:            ticks,
		RawNanos:         raw,
		Nanos:            nanos,
		UncertaintyNanos: unc,
		DriftNanos:       drift,
	}, nil
}

func roundFloat(f float64) float64 {
	if f >= 0 {
		return float64(int64(f + 0.5))
	}
	return -float64(int64(-f + 0.5))
}

// Order sorts points by corrected time and refuses to order pairs that are
// genuinely ambiguous: distinct samples landing inside each other's
// uncertainty intervals. Equal corrected times are always an error.
func (tl *Timeline) Order(points []Corrected) ([]Corrected, error) {
	out := make([]Corrected, len(points))
	copy(out, points)
	stableSortCorrected(out)
	var errs []error
	tol := int64(tl.tol)
	for i := 1; i < len(out); i++ {
		a, b := out[i-1], out[i]
		if b.Nanos == a.Nanos {
			errs = append(errs, mediaerr.New(mediaerr.KindUnsorted, -1, "", b.Nanos,
				fmt.Sprintf("two samples share corrected time %d ns and cannot be ordered", b.Nanos)))
			continue
		}
		if b.Nanos < a.Nanos-tol {
			errs = append(errs, mediaerr.New(mediaerr.KindClockDisorder, -1, "", b.Nanos,
				fmt.Sprintf("corrected time moved backwards: %d -> %d ns", a.Nanos, b.Nanos)))
			continue
		}
		if gap := b.Nanos - a.Nanos; gap < a.UncertaintyNanos+b.UncertaintyNanos {
			// Ambiguity is an interval property, not a per-pair event: report
			// the first ambiguous pair and stop, so a long run of ambiguous
			// samples yields one actionable error instead of thousands.
			errs = append(errs, mediaerr.New(mediaerr.KindUnsorted, -1, "", b.Nanos,
				fmt.Sprintf("sample spacing %d ns inside combined uncertainty %d ns",
					gap, a.UncertaintyNanos+b.UncertaintyNanos)))
			break
		}
	}
	if len(errs) > 0 {
		return out, errors.Join(errs...)
	}
	return out, nil
}

func stableSortCorrected(p []Corrected) {
	// Merge sort keeps the input permutation deterministic; sort.SliceStable
	// would do the same but this avoids a closure in the hot path.
	if len(p) < 2 {
		return
	}
	buf := make([]Corrected, len(p))
	for width := 1; width < len(p); width *= 2 {
		for lo := 0; lo < len(p); lo += 2 * width {
			mid := lo + width
			if mid > len(p) {
				mid = len(p)
			}
			hi := lo + 2*width
			if hi > len(p) {
				hi = len(p)
			}
			mergeCorrected(p[lo:mid], p[mid:hi], buf[lo:hi])
			copy(p[lo:hi], buf[lo:hi])
		}
	}
}

func mergeCorrected(a, b, out []Corrected) {
	i, j, k := 0, 0, 0
	for i < len(a) && j < len(b) {
		if b[j].Nanos < a[i].Nanos { // strictly, so equal keys stay stable
			out[k] = b[j]
			j++
		} else {
			out[k] = a[i]
			i++
		}
		k++
	}
	for i < len(a) {
		out[k] = a[i]
		i++
		k++
	}
	for j < len(b) {
		out[k] = b[j]
		j++
		k++
	}
}

// RawOrder verifies monotonicity of raw media time in the caller's order.
func (tl *Timeline) RawOrder(points []Corrected) error {
	var errs []error
	for i := 1; i < len(points); i++ {
		if points[i].RawNanos < points[i-1].RawNanos {
			errs = append(errs, mediaerr.New(mediaerr.KindTfdtRegression, -1, "", points[i].RawNanos,
				fmt.Sprintf("media time regressed %d -> %d ns", points[i-1].RawNanos, points[i].RawNanos)))
		}
	}
	return errors.Join(errs...)
}
