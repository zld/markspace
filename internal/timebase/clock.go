// Package timebase provides the shared integer-tick clock model:
// offset/drift estimation from sync anchors and wraparound unwrapping.
package timebase

import "math/big"

// TicksPerSecond is the unified integer timebase (90 kHz media clock).
const TicksPerSecond int64 = 90000

// Anchor pairs a source-clock reading with the reference-clock reading
// taken at the same instant.
type Anchor struct {
	Source int64 // source-clock ticks
	Ref    int64 // reference-clock ticks
}

// Clock estimates ref = offset + slope*source from anchors via exact
// rational least squares, so results are deterministic across runs.
type Clock struct {
	anchors []Anchor
	slope   *big.Rat
	offset  *big.Rat
	solved  bool
}

// AddAnchor registers a sync anchor.
func (c *Clock) AddAnchor(a Anchor) {
	c.anchors = append(c.anchors, a)
	c.solved = false
}

// Anchors returns the number of registered anchors.
func (c *Clock) Anchors() int { return len(c.anchors) }

func (c *Clock) solve() bool {
	if c.solved {
		return c.slope != nil
	}
	c.solved = true
	n := int64(len(c.anchors))
	if n == 0 {
		return false
	}
	xBar := big.NewRat(0, 1)
	yBar := big.NewRat(0, 1)
	for _, a := range c.anchors {
		xBar.Add(xBar, big.NewRat(a.Source, 1))
		yBar.Add(yBar, big.NewRat(a.Ref, 1))
	}
	xBar.Quo(xBar, big.NewRat(n, 1))
	yBar.Quo(yBar, big.NewRat(n, 1))
	num := big.NewRat(0, 1)
	den := big.NewRat(0, 1)
	tmp := new(big.Rat)
	for _, a := range c.anchors {
		dx := tmp.Sub(big.NewRat(a.Source, 1), xBar)
		dy := new(big.Rat).Sub(big.NewRat(a.Ref, 1), yBar)
		num.Add(num, new(big.Rat).Mul(dx, dy))
		den.Add(den, new(big.Rat).Mul(dx, dx))
	}
	if den.Sign() == 0 {
		// All anchors at one source instant: slope 1, offset from mean.
		c.slope = big.NewRat(1, 1)
	} else {
		c.slope = new(big.Rat).Quo(num, den)
	}
	c.offset = new(big.Rat).Sub(yBar, new(big.Rat).Mul(c.slope, xBar))
	return true
}

// DriftPPM returns round((slope-1)*1e6); ok is false without anchors.
func (c *Clock) DriftPPM() (ppm int64, ok bool) {
	if !c.solve() {
		return 0, false
	}
	r := new(big.Rat).Sub(c.slope, big.NewRat(1, 1))
	r.Mul(r, big.NewRat(1000000, 1))
	return roundRat(r), true
}

// ToRef maps source ticks to reference ticks; ok is false without anchors.
func (c *Clock) ToRef(src int64) (ref int64, ok bool) {
	if !c.solve() {
		return 0, false
	}
	r := new(big.Rat).Mul(c.slope, big.NewRat(src, 1))
	r.Add(r, c.offset)
	return roundRat(r), true
}

// roundRat rounds to the nearest integer, halves away from zero.
func roundRat(r *big.Rat) int64 {
	half := big.NewRat(1, 2)
	adj := new(big.Rat)
	if r.Sign() >= 0 {
		adj.Add(r, half)
	} else {
		adj.Sub(r, half)
	}
	q := new(big.Int).Quo(adj.Num(), adj.Denom()) // truncation toward zero after shift
	return q.Int64()
}
