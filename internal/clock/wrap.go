// Package clock normalizes media clocks across tracks and network nodes.
package clock

import (
	"errors"
	"fmt"
	"sort"

	"mediad/internal/core"
)

// WrapPeriod is the modulus of a 32-bit RTP timestamp clock.
const WrapPeriod int64 = 1 << 32

// Extender turns a wrapping 32-bit clock into a monotonic 64-bit counter.
// The raw value is never modified; callers keep both forms. A step is treated
// as a wrap only when the signed 32-bit delta is positive while the new raw
// value is numerically below the previous one, which distinguishes rollover
// from genuine regression.
type Extender struct {
	started bool
	lastRaw uint32
	ext     int64
	wraps   int
}

// WrapEvent records one rollover for auditing.
type WrapEvent struct {
	Ordinal  int
	At       int64 // extended timestamp after the wrap
	RawAfter uint32
	Delta    int64
}

// Extend returns the extended value and whether this step wrapped.
func (e *Extender) Extend(raw uint32) (int64, bool) {
	if !e.started {
		e.started = true
		e.lastRaw = raw
		e.ext = int64(raw)
		return e.ext, false
	}
	delta := int64(int32(raw - e.lastRaw))
	wrapped := delta > 0 && raw < e.lastRaw
	e.ext += delta
	e.lastRaw = raw
	if wrapped {
		e.wraps++
	}
	return e.ext, wrapped
}

// Wraps returns how many rollovers have been observed.
func (e *Extender) Wraps() int { return e.wraps }

// DriftSample pairs a trusted source clock with the same instant read on a
// node, both in seconds.
type DriftSample struct {
	Source core.Rational
	Node   core.Rational
}

// DriftModel is a least-squares linear fit node = a*source + b. The
// correction maps a node reading back onto the source clock:
// source ≈ (node - b) / a. Every field is retained so the correction is
// auditable rather than silently applied.
type DriftModel struct {
	Samples     int           `json:"samples"`
	Slope       core.Rational `json:"slope"`     // node seconds per source second
	Intercept   core.Rational `json:"intercept"` // node seconds at source 0
	RatePPB     int64         `json:"rate_ppb"`  // (slope-1)*1e9, rounded
	MaxResidual core.Rational `json:"max_residual"`
	Node        string        `json:"node"`
	Applied     bool          `json:"applied"`
}

// ErrInsufficientSamples is returned when a drift fit has fewer than two
// distinct source times.
func ErrInsufficientSamples() error {
	return errors.New("clock: need at least two distinct drift samples")
}

// FitDrift performs an exact least-squares fit in rational arithmetic.
func FitDrift(node string, samples []DriftSample) (DriftModel, error) {
	if len(samples) < 2 {
		return DriftModel{}, ErrInsufficientSamples()
	}
	sorted := make([]DriftSample, len(samples))
	copy(sorted, samples)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Source.Cmp(sorted[j].Source) < 0 })
	n := int64(len(sorted))
	sumX, sumY := core.Zero(), core.Zero()
	sumXX, sumXY := core.Zero(), core.Zero()
	for _, s := range sorted {
		sumX = sumX.Add(s.Source)
		sumY = sumY.Add(s.Node)
		sumXX = sumXX.Add(s.Source.Mul(s.Source))
		sumXY = sumXY.Add(s.Source.Mul(s.Node))
	}
	nr := core.Rat(n, 1)
	den := nr.Mul(sumXX).Sub(sumX.Mul(sumX))
	if den.Zero() {
		return DriftModel{}, ErrInsufficientSamples()
	}
	slope := nr.Mul(sumXY).Sub(sumX.Mul(sumY)).Div(den)
	intercept := sumY.Sub(slope.Mul(sumX)).Div(nr)
	model := DriftModel{
		Samples:     len(sorted),
		Slope:       slope,
		Intercept:   intercept,
		MaxResidual: core.Zero(),
		Node:        node,
	}
	model.RatePPB = roundDiv((slope.Num-slope.Den)*1_000_000_000, slope.Den)
	for _, s := range sorted {
		pred := slope.Mul(s.Source).Add(intercept)
		r := pred.Sub(s.Node).Abs()
		if r.Cmp(model.MaxResidual) > 0 {
			model.MaxResidual = r
		}
	}
	return model, nil
}

func roundDiv(num, den int64) int64 {
	if den < 0 {
		num, den = -num, -den
	}
	if num >= 0 {
		return (num + den/2) / den
	}
	return -((-num + den/2) / den)
}

// Correct maps a node clock reading back onto source time.
func (m DriftModel) Correct(node core.Rational) core.Rational {
	if !m.Applied || m.Slope.Zero() {
		return node
	}
	return node.Sub(m.Intercept).Div(m.Slope)
}

// DriftCorrector applies a per-node model while preserving monotonicity.
// The first corrected value defines the origin; later values never regress
// below the previous emitted value, even across a wrap boundary.
type DriftCorrector struct {
	Model    DriftModel
	started  bool
	lastNorm core.Rational
	clamps   int
	applied  int
}

// NewCorrector builds a corrector for one node.
func NewCorrector(model DriftModel) *DriftCorrector {
	return &DriftCorrector{Model: model}
}

// Correct returns the normalized clock for raw node time t. It reports
// whether the input wrapped and whether monotonic clamping was required.
func (c *DriftCorrector) Correct(node core.Rational, wrapped bool) (norm core.Rational, clamped bool) {
	norm = c.Model.Correct(node)
	if !c.started {
		c.started = true
		c.lastNorm = norm
		c.applied++
		return norm, false
	}
	if norm.Cmp(c.lastNorm) < 0 {
		norm = c.lastNorm
		c.clamps++
		clamped = true
	}
	c.lastNorm = norm
	c.applied++
	return norm, clamped
}

// Clamps returns how many monotonic guards fired.
func (c *DriftCorrector) Clamps() int { return c.clamps }

// Applied returns how many samples were corrected.
func (c *DriftCorrector) Applied() int { return c.applied }

// Validate checks a model is self-consistent and safe to apply.
func (m DriftModel) Validate() error {
	if m.Slope.Zero() {
		return errors.New("clock: zero slope in drift model")
	}
	if m.Slope.Num < 0 {
		return errors.New("clock: negative slope in drift model")
	}
	// A correction larger than one hour over the whole fit is almost always a
	// bad fit; reject rather than silently shifting media.
	if m.Intercept.Abs().Cmp(core.Rat(3600, 1)) > 0 {
		return fmt.Errorf("clock: implausible drift intercept %s", m.Intercept)
	}
	return nil
}
