// Package clock unifies integer media timescales, estimates clock drift with
// bounded estimators and unwraps finite-width timestamps only when modulus
// boundary evidence supports it.
package clock

import (
	"errors"
	"math"
	"math/big"

	"mediascan/internal/model"
)

// UnifiedHz is the target integer tick rate.
const UnifiedHz = model.UnifiedTimescale

var (
	// ErrZeroTimescale means a box or manifest declares a zero timescale.
	ErrZeroTimescale = errors.New("clock: zero timescale")
	// ErrOverflow means a conversion cannot be represented in int64 ticks.
	ErrOverflow = errors.New("clock: tick value overflow")
)

// Convert maps ticks expressed at fromHz into toHz using integer arithmetic
// with round-to-nearest. Zero timescales are rejected and results that do
// not fit int64 are reported as overflow rather than wrapped.
func Convert(ticks int64, fromHz, toHz uint32) (int64, error) {
	if fromHz == 0 || toHz == 0 {
		return 0, ErrZeroTimescale
	}
	if ticks == 0 {
		return 0, nil
	}
	neg := ticks < 0
	t := big.NewInt(ticks)
	if neg {
		t.Abs(t)
	}
	num := new(big.Int).Mul(t, big.NewInt(int64(toHz)))
	den := big.NewInt(int64(fromHz))
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	// round to nearest: 2*rem >= den -> q+1
	twiceRem := new(big.Int).Lsh(rem, 1)
	if twiceRem.Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, ErrOverflow
	}
	out := q.Int64()
	if neg {
		out = -out
	}
	return out, nil
}

// ToUnified converts source-timescale ticks to the unified timeline.
func ToUnified(ticks int64, fromHz uint32) (int64, error) {
	return Convert(ticks, fromHz, UnifiedHz)
}

// MustUnified is for trusted constants; it panics on error.
func MustUnified(ticks int64, fromHz uint32) int64 {
	v, err := ToUnified(ticks, fromHz)
	if err != nil {
		panic(err)
	}
	return v
}

// SafeUnified converts and turns failures into a model.Fault instead of
// aborting the whole analysis. Returns 0 when conversion is impossible.
func SafeUnified(ticks int64, fromHz uint32, seg string) (int64, []model.Fault) {
	v, err := ToUnified(ticks, fromHz)
	if err == nil {
		return v, nil
	}
	f := model.NewFault(model.CodeZeroTimescale, seg, err.Error())
	if errors.Is(err, ErrOverflow) {
		f.Code = model.CodeTickOverflow
	}
	f.TimeStart, f.TimeEnd = model.Unknown, model.Unknown
	return 0, []model.Fault{f}
}

// DriftEstimator fits a bounded linear rate (media vs reference) over a
// bounded observation window. The returned rate is expressed in parts per
// million and clamped to MaxAbsPPM.
type DriftEstimator struct {
	MaxAbsPPM int64
	Window    int
	refs      []int64
	medias    []int64
}

// NewDriftEstimator creates an estimator with sane bounded defaults:
// at most 4096 observations and |drift| <= 100000 ppm (10%).
func NewDriftEstimator(maxAbsPPM int64) *DriftEstimator {
	if maxAbsPPM <= 0 {
		maxAbsPPM = 100_000
	}
	return &DriftEstimator{MaxAbsPPM: maxAbsPPM, Window: 4096}
}

// Observe appends one (reference, media) pair, evicting the oldest pair when
// the bounded window is full.
func (d *DriftEstimator) Observe(reference, media int64) {
	if d.Window <= 0 {
		d.Window = 4096
	}
	d.refs = append(d.refs, reference)
	d.medias = append(d.medias, media)
	if len(d.refs) > d.Window {
		d.refs = d.refs[len(d.refs)-d.Window:]
		d.medias = d.medias[len(d.medias)-d.Window:]
	}
}

// Len returns the number of retained observations.
func (d *DriftEstimator) Len() int { return len(d.refs) }

// EstimatePPM returns the clamped drift rate. ok is false when there is not
// enough non-degenerate evidence. Integer exact arithmetic is used so the
// result is reproducible run to run.
func (d *DriftEstimator) EstimatePPM() (ppm int64, ok bool) {
	n := len(d.refs)
	if n < 2 {
		return 0, false
	}
	N := big.NewInt(int64(n))
	var sumR, sumM, sumRM, sumR2 big.Int
	for i := 0; i < n; i++ {
		r := big.NewInt(d.refs[i])
		m := big.NewInt(d.medias[i])
		sumR.Add(&sumR, r)
		sumM.Add(&sumM, m)
		rm := new(big.Int).Mul(r, m)
		sumRM.Add(&sumRM, rm)
		r2 := new(big.Int).Mul(r, r)
		sumR2.Add(&sumR2, r2)
	}
	// denom = n*sumR2 - sumR^2 ; num = n*sumRM - sumR*sumM
	den := new(big.Int).Sub(new(big.Int).Mul(N, &sumR2), new(big.Int).Mul(&sumR, &sumR))
	if den.Sign() == 0 {
		return 0, false
	}
	num := new(big.Int).Sub(new(big.Int).Mul(N, &sumRM), new(big.Int).Mul(&sumR, &sumM))
	// ppm = (num/den - 1) * 1e6 = (num-den)*1e6 / den
	diff := new(big.Int).Sub(num, den)
	ppmBig := new(big.Int).Quo(new(big.Int).Mul(diff, big.NewInt(1_000_000)), den)
	if !ppmBig.IsInt64() {
		if ppmBig.Sign() > 0 {
			return d.MaxAbsPPM, true
		}
		return -d.MaxAbsPPM, true
	}
	p := ppmBig.Int64()
	if p > d.MaxAbsPPM {
		p = d.MaxAbsPPM
	} else if p < -d.MaxAbsPPM {
		p = -d.MaxAbsPPM
	}
	return p, true
}

// ApplyPPM scales ticks by (1_000_000 + ppm) / 1_000_000.
func ApplyPPM(ticks int64, ppm int64) int64 {
	num := new(big.Int).Mul(big.NewInt(ticks), big.NewInt(1_000_000+ppm))
	out := new(big.Int).Quo(num, big.NewInt(1_000_000))
	if !out.IsInt64() {
		if ticks >= 0 {
			return math.MaxInt64
		}
		return math.MinInt64
	}
	return out.Int64()
}
