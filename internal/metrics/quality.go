// Package metrics computes reproducible bitrate, SNR and alignment metrics.
package metrics

import (
	"math"

	"mediacheck/internal/timebase"
)

// MaxSNRDB is the finite cap used when quantization noise is mathematically
// zero. JSON has no stable representation for +Inf.
const MaxSNRDB = 160.0

// ActualBitrate computes PCM payload bitrate over frameCount frames.
func ActualBitrate(payloadBytes, frameCount int64, sampleRate int) (float64, error) {
	if payloadBytes < 0 || frameCount <= 0 || sampleRate <= 0 {
		return 0, timebase.NewError(timebase.CodeInvalidArgument, "invalid bitrate inputs")
	}
	return float64(payloadBytes*8) * float64(sampleRate) / float64(frameCount), nil
}

// DeltaPercent returns signed percentage difference actual relative target.
func DeltaPercent(targetBPS int64, actualBPS float64) (float64, error) {
	if targetBPS <= 0 {
		return 0, timebase.NewError(timebase.CodeInvalidArgument, "target bitrate must be positive")
	}
	return (actualBPS - float64(targetBPS)) / float64(targetBPS) * 100, nil
}

// QuantizeLeftAligned reduces canonical int32 samples to 16 or 24 bits and
// returns the reconstructed left-aligned int32 values used for SNR checks.
func QuantizeLeftAligned(samples []int32, bits int) ([]int32, error) {
	if bits != 16 && bits != 24 {
		return nil, timebase.NewError(timebase.CodeInvalidArgument, "quantization bits must be 16 or 24")
	}
	shift := uint(32 - bits)
	round := int64(1) << (shift - 1)
	out := make([]int32, len(samples))
	for i, s := range samples {
		v := int64(s)
		var q int64
		if v >= 0 {
			q = ((v + round) >> shift) << shift
		} else {
			// Symmetric rounding avoids a DC bias on negative samples.
			q = -(((-v + round - 1) >> shift) << shift)
		}
		if q > math.MaxInt32 {
			q = math.MaxInt32
		}
		if q < math.MinInt32 {
			q = math.MinInt32
		}
		out[i] = int32(q)
	}
	return out, nil
}

// SNR returns signal-to-noise ratio in dB over the common prefix. Identical
// signals return MaxSNRDB rather than +Inf.
func SNR(reference, delivered []int32) float64 {
	n := len(reference)
	if len(delivered) < n {
		n = len(delivered)
	}
	var signal, noise float64
	for i := 0; i < n; i++ {
		r := float64(reference[i])
		d := float64(delivered[i])
		signal += r * r
		e := r - d
		noise += e * e
	}
	if noise == 0 {
		return MaxSNRDB
	}
	if signal == 0 {
		return 0
	}
	return 10 * math.Log10(signal/noise)
}

// Alignment is the result of a bounded sample-lag search.
type Alignment struct {
	ShiftSamples int     `json:"shift_samples"`
	ErrorSamples int     `json:"align_err_samples"`
	SNRDB        float64 `json:"snr_db"`
	Compared     int     `json:"compared_samples"`
}

// AlignAndSNR searches integer sample shifts in [-maxShift,maxShift]. Positive
// shift means delivered samples are moved forward for comparison. The returned
// error is the absolute residual alignment offset in samples.
func AlignAndSNR(reference, delivered []int32, maxShift int) Alignment {
	if maxShift < 0 {
		maxShift = 0
	}
	best := Alignment{ShiftSamples: 0, SNRDB: -math.Inf(1)}
	for shift := -maxShift; shift <= maxShift; shift++ {
		var a, b []int32
		if shift >= 0 {
			if shift >= len(delivered) {
				continue
			}
			a = reference
			b = delivered[shift:]
		} else {
			off := -shift
			if off >= len(reference) {
				continue
			}
			a = reference[off:]
			b = delivered
		}
		n := len(a)
		if len(b) < n {
			n = len(b)
		}
		if n == 0 {
			continue
		}
		snr := SNR(a[:n], b[:n])
		if snr > best.SNRDB {
			best = Alignment{ShiftSamples: shift, SNRDB: snr, Compared: n}
		}
	}
	best.ErrorSamples = absInt(best.ShiftSamples)
	if math.IsInf(best.SNRDB, 0) {
		best.SNRDB = 0
	}
	return best
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
