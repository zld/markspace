// Package clock provides a unified integer media timebase, rescaling
// between track timescales, wrap-around extension for RTP and container
// timestamps, and drift calibration against a configurable threshold.
package clock

import (
	"math/big"
	"math/bits"
)

// UnifiedTimescale is the common integer tick scale (ticks per second)
// every track timestamp is converted to before mapping or remuxing.
const UnifiedTimescale = 90000

// Convert rescales ticks from one timescale to another, rounding to the
// nearest tick (half up). A zero source scale yields zero; callers must
// validate timescales before converting.
func Convert(ticks uint64, from, to uint32) uint64 {
	if from == 0 {
		return 0
	}
	if from == to {
		return ticks
	}
	hi, lo := bits.Mul64(ticks, uint64(to))
	if hi != 0 {
		// Pathological magnitude; fall back to big.Int.
		b := new(big.Int).SetUint64(ticks)
		b.Mul(b, new(big.Int).SetUint64(uint64(to)))
		b.Add(b, new(big.Int).SetUint64(uint64(from/2)))
		b.Div(b, new(big.Int).SetUint64(uint64(from)))
		return b.Uint64()
	}
	f := uint64(from)
	q, rem := lo/f, lo%f
	if rem >= f-rem {
		q++
	}
	return q
}

// ConvertSigned rescales a signed tick offset between timescales.
func ConvertSigned(ticks int64, from, to uint32) int64 {
	if ticks < 0 {
		return -int64(Convert(uint64(-ticks), from, to))
	}
	return int64(Convert(uint64(ticks), from, to))
}

// Extend maps a wrapped fixed-width counter (RTP sequence numbers: 16 bits,
// RTP/container timestamps: 32 bits) onto a monotonically increasing int64,
// using prev as the previously extended value. A negative prev seeds the
// extension with the raw value itself.
func Extend(prev int64, raw uint64, width uint) int64 {
	if prev < 0 {
		return int64(raw)
	}
	cycle := int64(1) << width
	half := cycle >> 1
	diff := (int64(raw) - prev) & (cycle - 1)
	if diff >= half {
		diff -= cycle
	}
	return prev + diff
}

// ExtendRTP extends a 32-bit RTP timestamp across wrap-around.
func ExtendRTP(prev int64, raw uint32) int64 { return Extend(prev, uint64(raw), 32) }

// ExtendRTPSeq extends a 16-bit RTP sequence number across wrap-around.
func ExtendRTPSeq(prev int64, raw uint16) int64 { return Extend(prev, uint64(raw), 16) }

// DriftReport captures a per-track drift calibration result.
type DriftReport struct {
	TrackID     string `json:"track_id"`
	DevBefore   int64  `json:"dev_before_ticks"` // measured first tick minus reference
	DevAfter    int64  `json:"dev_after_ticks"`  // residual after alignment
	Offset      int64  `json:"offset_ticks"`     // applied correction
	WithinLimit bool   `json:"within_limit"`
}

// Calibrate computes the constant offset aligning a track's first tick to
// the reference first tick. If the pre-calibration deviation exceeds
// threshold (in unified ticks), WithinLimit is false and no offset is
// applied; the caller is expected to fail with the track position kept.
func Calibrate(trackID string, reference, measuredFirst, threshold int64) DriftReport {
	rep := DriftReport{TrackID: trackID, DevBefore: measuredFirst - reference}
	dev := rep.DevBefore
	if dev < 0 {
		dev = -dev
	}
	if dev > threshold {
		rep.DevAfter = rep.DevBefore
		return rep
	}
	rep.WithinLimit = true
	rep.Offset = reference - measuredFirst
	rep.DevAfter = 0
	return rep
}
