// Package syncer estimates capture-clock drift, maps variable-frame-rate
// video samples onto the shared integer-nanosecond time base and aligns audio
// blocks with video frames. Every correction is deterministic: floating point
// is used only inside the bounded regression, and the applied mapping is an
// exact rational integer transform, so repeated runs hash identically.
package syncer

import (
	"math"

	"mediacore"
)

// maxAnchors bounds the regression window so estimation cannot grow without
// limit on a long-lived stream.
const maxAnchors = 128

// Anchor pairs a device-clock reading with the master-clock reading observed
// at the same instant (e.g. derived from an RTCP sender report).
type Anchor struct {
	Device int64
	Master int64
}

// DriftEstimator is an online, bounded least-squares estimator of the ratio
// between a capture clock and the master clock.
type DriftEstimator struct {
	track  mediacore.TrackID
	maxPPM int64 // configured ppm bound; above it the track is not auto-aligned

	anchors                          []Anchor
	baseDevice, baseMaster           int64
	ppmFixed                         int64 // applied drift in parts per million
	rawPPMFixed                      int64 // unclamped estimate in parts per million
	alignable, estimateReady, capped bool
}

// NewDriftEstimator builds an estimator. maxPPM is the absolute drift bound
// that may be corrected automatically.
func NewDriftEstimator(track mediacore.TrackID, maxPPM int64) *DriftEstimator {
	return &DriftEstimator{track: track, maxPPM: maxPPM, alignable: true}
}

// Observe records one clock anchor and refits the estimate online.
func (d *DriftEstimator) Observe(a Anchor) {
	if len(d.anchors) == 0 {
		d.baseDevice, d.baseMaster = a.Device, a.Master
	}
	d.anchors = append(d.anchors, a)
	if len(d.anchors) > maxAnchors {
		// Drop the oldest anchor while keeping a stable origin.
		d.anchors = d.anchors[1:]
	}
	if len(d.anchors) < 2 {
		return
	}
	d.refit()
}

// refit runs ordinary least squares on integer-safe nanosecond deltas. The
// slope s = Δdevice/Δmaster; drift ppm = (s-1)*1e6.
func (d *DriftEstimator) refit() {
	var sumX, sumY, sumXY, sumXX float64
	n := float64(len(d.anchors))
	for _, a := range d.anchors {
		x := float64(a.Master - d.baseMaster)
		y := float64(a.Device - d.baseDevice)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	den := n*sumXX - sumX*sumX
	if den == 0 {
		return
	}
	slope := (n*sumXY - sumX*sumY) / den
	d.rawPPMFixed = int64(math.Round((slope - 1.0) * 1_000_000))
	d.estimateReady = true
	d.ppmFixed = d.rawPPMFixed
	d.capped = false
	if abs64(d.ppmFixed) > d.maxPPM {
		d.alignable = false
		d.ppmFixed = clamp64(d.ppmFixed, d.maxPPM)
		d.capped = true
	}
}

// PPM returns the applied (clamped) drift in parts per million, the raw
// estimate and whether the track remains auto-alignable.
func (d *DriftEstimator) PPM() (applied, raw int64, alignable bool) {
	return d.ppmFixed, d.rawPPMFixed, d.alignable
}

// Alignable reports whether the observed drift stayed inside the ppm bound.
func (d *DriftEstimator) Alignable() bool { return d.alignable }

// Map translates one device time to the master clock with the exact rational
// transform  master = base + (device-base)*1e6/(1e6+ppm), retaining full
// integer determinism. When the drift exceeds the bound it still returns the
// clamped mapping together with alignable=false so callers can flag it.
func (d *DriftEstimator) Map(deviceTime int64) (mapped int64, alignable bool) {
	if !d.estimateReady {
		return deviceTime, d.alignable
	}
	delta := deviceTime - d.baseDevice
	den := int64(1_000_000) + d.ppmFixed
	if den <= 0 {
		// Pathological clock inversion: refuse extrapolation.
		return d.baseMaster, false
	}
	mapped = d.baseMaster + mulDiv(delta, 1_000_000, den)
	return mapped, d.alignable
}

// StateError returns a classified drift error when the track cannot be
// aligned automatically, otherwise nil.
func (d *DriftEstimator) StateError() error {
	if d.alignable || !d.estimateReady {
		return nil
	}
	return mediacore.NewError(mediacore.CatDrift, d.track, "drift.estimate", 0,
		"observed drift exceeds configured ppm bound")
}

// mulDiv computes a*b/c with 128-bit intermediate width via math/big-free
// splitting, keeping signs and rounding toward zero.
func mulDiv(a, b, c int64) int64 {
	if a == 0 || b == 0 {
		return 0
	}
	neg := (a < 0) != (b < 0)
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	// Split into high/low 32-bit parts to avoid float and overflow.
	ah, al := uint64(a)>>32, uint64(a)&0xffffffff
	bh, bl := uint64(b)>>32, uint64(b)&0xffffffff
	lo := al * bl
	mid := ah*bl + al*bh
	hi := ah*bh + mid>>32
	midLo := mid << 32
	// Sum into a virtual 128-bit number (hi, low64).
	low64 := lo + midLo
	if low64 < lo {
		hi++
	}
	q, _ := div128(hi, low64, c)
	if neg {
		return -q
	}
	return q
}

// div128 divides the unsigned 128-bit numerator (hi:lo) by c. It returns the
// quotient and remainder; callers guarantee the quotient fits int64.
func div128(hi, lo uint64, c int64) (int64, uint64) {
	if hi == 0 {
		return int64(lo / uint64(c)), lo % uint64(c)
	}
	// Long division one bit at a time (bounded to 64 high bits; rare path).
	var rem uint64
	var qHi, qLo uint64
	for i := 63; i >= 0; i-- {
		rem = rem<<1 | (hi>>uint(i))&1
		if rem >= uint64(c) {
			rem -= uint64(c)
			qHi |= 1 << uint(i)
		}
	}
	for i := 63; i >= 0; i-- {
		rem = rem<<1 | (lo>>uint(i))&1
		if rem >= uint64(c) {
			rem -= uint64(c)
			qLo |= 1 << uint(i)
		}
	}
	return int64(qLo), rem
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func clamp64(v, bound int64) int64 {
	if v > bound {
		return bound
	}
	if v < -bound {
		return -bound
	}
	return v
}
