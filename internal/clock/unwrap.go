package clock

import (
	"fmt"
	"math"
	"sort"
)

// Unwrapper expands a modulo-N timestamp series into a monotonic timeline.
// It is shared by RTP's 32-bit media clock and by MP4 decode-time sample
// tables that wrap at 2^32.
type Unwrapper struct {
	Modulus uint64

	started    bool
	lastRaw    uint64
	lastExpand uint64
	wrapCount  int
}

// UnwrapResult describes one expanded timestamp. Value is always derived from
// the raw value on the first lap plus the number of completed wraps; the raw
// input is never modified.
type UnwrapResult struct {
	Raw       uint64
	Value     uint64
	Wrapped   bool
	Regressed bool
	Duplicate bool
	Missing   uint64
}

// NewUnwrapper creates an unwrapper with the given modulus (e.g. 1<<32).
func NewUnwrapper(modulus uint64) *Unwrapper {
	if modulus == 0 {
		modulus = 1 << 32
	}
	return &Unwrapper{Modulus: modulus}
}

// Wraps returns how many wrap boundaries were crossed.
func (u *Unwrapper) Wraps() int { return u.wrapCount }

// Push expands the next raw timestamp from arrival order.
func (u *Unwrapper) Push(raw uint64) UnwrapResult {
	res := UnwrapResult{Raw: raw}
	if !u.started {
		u.started = true
		u.lastRaw = raw
		u.lastExpand = raw
		res.Value = raw
		return res
	}
	half := u.Modulus / 2
	switch {
	case raw == u.lastRaw:
		res.Duplicate = true
		res.Value = u.lastExpand
	case raw > u.lastRaw && raw-u.lastRaw <= half:
		delta := raw - u.lastRaw
		if delta > 1 {
			res.Missing = delta - 1
		}
		u.lastRaw = raw
		u.lastExpand += delta
		res.Value = u.lastExpand
	case raw < u.lastRaw && u.lastRaw-raw > half:
		// Forward wrap across the modulus boundary.
		u.wrapCount++
		delta := (u.Modulus - u.lastRaw) + raw
		u.lastRaw = raw
		u.lastExpand += delta
		res.Wrapped = true
		res.Value = u.lastExpand
	case raw < u.lastRaw:
		// Small step backwards: an out-of-order packet or timestamp
		// regression. The baseline is intentionally kept so a replay of
		// the previous position does not shift the whole timeline.
		res.Regressed = true
		res.Value = u.lastExpand - (u.lastRaw - raw)
	default:
		// raw > lastRaw by more than half the modulus: the value belongs
		// to the previous lap. Report it as a regression rather than
		// inventing a forward jump.
		res.Regressed = true
		res.Value = u.lastExpand - (u.Modulus - (raw - u.lastRaw))
	}
	return res
}

// DriftSample is one (reference, measured) pair on the tick base.
type DriftSample struct {
	At   Tick
	Meas Tick
}

// DriftEstimator fits measured-vs-reference offset with least squares and
// reports whether the drift stays within the 100 ms / 10 min contract.
type DriftEstimator struct {
	samples []DriftSample
}

// Add records a reference/media pair.
func (d *DriftEstimator) Add(at, meas Tick) {
	d.samples = append(d.samples, DriftSample{At: at, Meas: meas})
}

// Len returns the sample count.
func (d *DriftEstimator) Len() int { return len(d.samples) }

// OffsetAt returns the fitted measured-minus-reference offset.
func (d *DriftEstimator) OffsetAt(at Tick) (Tick, bool) {
	if len(d.samples) == 0 {
		return 0, false
	}
	if len(d.samples) == 1 {
		return d.samples[0].Meas - d.samples[0].At, true
	}
	n := float64(len(d.samples))
	sx, sy, sxx, sxy := 0.0, 0.0, 0.0, 0.0
	for _, s := range d.samples {
		x := float64(s.At)
		y := float64(s.Meas - s.At)
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return Tick(math.Round(sy / n)), true
	}
	slope := (n*sxy - sx*sy) / den
	intercept := (sy - slope*sx) / n
	return Tick(math.Round(slope*float64(at) + intercept)), true
}

// DriftBoundPer is the maximum permitted correction: 100 ms per 10 minutes.
const DriftBoundPer = 100 * (TickRate / 1000)

// DriftWindow is the duration over which DriftBoundPer applies.
const DriftWindow = 10 * 60 * Tick(TickRate)

// DriftLimit returns the permitted absolute drift after elapsed time.
func DriftLimit(elapsed Tick) Tick {
	if elapsed <= 0 {
		return 0
	}
	return Tick(int64(elapsed) * int64(DriftBoundPer) / int64(DriftWindow))
}

// Correction is the outcome of drift analysis. Original timestamps are never
// rewritten: Applied is advisory and Exceeded records the violation.
type Correction struct {
	Measured     Tick
	Limit        Tick
	Applied      Tick
	Exceeded     bool
	At           Tick
	OriginalLast Tick
}

// Analyze classifies drift and computes the bounded correction.
func (d *DriftEstimator) Analyze() (Correction, bool) {
	if len(d.samples) == 0 {
		return Correction{}, false
	}
	if len(d.samples) == 1 {
		s := d.samples[0]
		return Correction{Measured: s.Meas - s.At, At: s.At, OriginalLast: s.Meas}, true
	}
	ss := append([]DriftSample(nil), d.samples...)
	sort.SliceStable(ss, func(i, j int) bool {
		if ss[i].At != ss[j].At {
			return ss[i].At < ss[j].At
		}
		return ss[i].Meas < ss[j].Meas
	})
	first, last := ss[0], ss[len(ss)-1]
	off, _ := d.OffsetAt(last.At)
	limit := DriftLimit(last.At - first.At)
	c := Correction{
		Measured:     off,
		Limit:        limit,
		At:           last.At,
		OriginalLast: last.Meas,
	}
	if off > limit || off < -limit {
		c.Exceeded = true
		return c, true
	}
	c.Applied = off
	return c, true
}

// Scope returns a human-stable description of the drift bound.
func (c Correction) Scope() string {
	return fmt.Sprintf("measured=%s limit=%s apply=%s", c.Measured, c.Limit, c.Applied)
}
