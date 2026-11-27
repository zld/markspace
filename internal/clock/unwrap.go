package clock

import (
	"math"

	merr "mediatool/internal/errors"
)

// Unwrapper32 extends 32-bit media timestamps (RTP clocks, mvhd-style
// counters) to full 64-bit values. A modular wrap is expanded only when the
// signed modular delta lies inside the half-range window; a delta outside
// that window is not provably continuous and is rejected instead of guessed.
type Unwrapper32 struct {
	last      uint64
	init      bool
	maxJump   int64
	wrapCount uint64
}

// NewUnwrapper32 creates an unwrapper with the standard half-range bound.
func NewUnwrapper32() *Unwrapper32 {
	return &Unwrapper32{maxJump: int64(1) << 31}
}

// Unwrap returns the 64-bit extension of v.
func (u *Unwrapper32) Unwrap(v uint32) (uint64, error) {
	if !u.init {
		u.init = true
		u.last = uint64(v)
		return u.last, nil
	}
	rawLast := uint32(u.last)
	delta := int64(int32(uint32(v) - rawLast))
	if delta == 0 {
		return u.last, nil
	}
	if delta <= -u.maxJump || delta >= u.maxJump {
		return 0, merr.New(merr.CodeWrapBroken,
			"32-bit counter jump %d outside half-range window", delta)
	}
	// A positive modular delta with a smaller raw value means a wrap.
	if delta > 0 && v < rawLast {
		u.wrapCount++
	}
	next := int64(u.last) + delta
	if next < 0 {
		return 0, merr.New(merr.CodeWrapBroken, "unwrapped value underflowed 64-bit range")
	}
	u.last = uint64(next)
	return u.last, nil
}

// Wraps reports how many times the counter has wrapped around.
func (u *Unwrapper32) Wraps() uint64 { return u.wrapCount }

// Unwrapper16 unwraps 16-bit RTP sequence numbers with the same half-range
// continuity rule as [Unwrapper32].
type Unwrapper16 struct {
	last    uint32
	init    bool
	maxJump int32
}

// NewUnwrapper16 creates a sequence-number unwrapper (window 32768).
func NewUnwrapper16() *Unwrapper16 {
	return &Unwrapper16{maxJump: int32(1) << 15}
}

// Unwrap extends a 16-bit sequence number to 32 bits.
func (u *Unwrapper16) Unwrap(seq uint16) (uint32, error) {
	if !u.init {
		u.init = true
		u.last = uint32(seq)
		return u.last, nil
	}
	delta := int32(int16(uint16(seq) - uint16(u.last)))
	if delta <= -u.maxJump || delta >= u.maxJump {
		return 0, merr.New(merr.CodeWrapBroken,
			"16-bit sequence jump %d outside half-range window", delta)
	}
	next := int64(u.last) + int64(delta)
	if next < 0 || next > math.MaxUint32 {
		return 0, merr.New(merr.CodeWrapBroken, "unwrapped sequence out of 32-bit range")
	}
	u.last = uint32(next)
	return u.last, nil
}

// DriftEstimator fits remote vs local timestamps with ordinary least squares
// and reports the clock skew. Placement decisions remain integer; only the
// estimated rate uses floating point.
type DriftEstimator struct {
	localAnchor int64
	remoteAnchor int64
	n            float64
	sumX, sumY, sumXY, sumX2 float64
}

// Add records one (local nanos, remote nanos) observation.
func (d *DriftEstimator) Add(local, remote int64) {
	if d.n == 0 {
		d.localAnchor = local
		d.remoteAnchor = remote
	}
	x := float64(local - d.localAnchor)
	y := float64(remote - d.remoteAnchor) - x
	d.n++
	d.sumX += x
	d.sumY += y
	d.sumXY += x * y
	d.sumX2 += x * x
}

// RatePPM returns the estimated skew in parts per million. Positive means the
// remote clock runs fast. Zero is returned until two points exist.
func (d *DriftEstimator) RatePPM() float64 {
	if d.n < 2 {
		return 0
	}
	den := d.n*d.sumX2 - d.sumX*d.sumX
	if den == 0 {
		return 0
	}
	slope := (d.n*d.sumXY - d.sumX*d.sumY) / den
	return slope * 1e6
}

// OffsetNanos predicts (remote - local) at the given local timestamp.
func (d *DriftEstimator) OffsetNanos(local int64) int64 {
	if d.n == 0 {
		return 0
	}
	x := float64(local - d.localAnchor)
	var slope, intercept float64
	if d.n >= 2 {
		den := d.n*d.sumX2 - d.sumX*d.sumX
		if den != 0 {
			slope = (d.n*d.sumXY - d.sumX*d.sumY) / den
			intercept = (d.sumY - slope*d.sumX) / d.n
		}
	}
	return int64(math.Round(slope*x + intercept))
}

// RejectIfExceeds returns CodeDriftTooLarge when the estimated rate passes
// maxPPM in either direction.
func (d *DriftEstimator) RejectIfExceeds(maxPPM float64) error {
	rate := d.RatePPM()
	if math.Abs(rate) > maxPPM {
		return merr.New(merr.CodeDriftTooLarge,
			"clock drift %.3f ppm exceeds bound %.3f ppm", rate, maxPPM)
	}
	return nil
}
