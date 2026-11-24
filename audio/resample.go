package audio

import (
	"math"
	"math/big"

	"mediacheck/timeline"
)

// SampleStats reports the accounting one resampling run must produce.
type SampleStats struct {
	RateIn          int64
	RateOut         int64
	SamplesIn       int64
	SamplesOut      int64
	DurationInNs    int64 // exact duration implied by the input count
	DurationOutNs   int64
	DurationErrorNs int64 // accumulated drift: out-duration minus in-duration
}

// Plan computes the output sample count for a bounded input interval without
// touching sample data, and reports the accumulated duration error. Exact
// rational arithmetic guards every count against integer overflow.
func Plan(rateIn, rateOut, samplesIn int64) (SampleStats, error) {
	if rateIn <= 0 || rateOut <= 0 || samplesIn < 0 {
		return SampleStats{}, timeline.New(timeline.CatConfig,
			"resample rates must be positive and input count non-negative")
	}
	durIn, ok := timeline.NsFromUnits(samplesIn, rateIn)
	if !ok {
		return SampleStats{}, timeline.New(timeline.CatOverflow, "input duration overflow")
	}
	samplesOut, ok := ratioRound(samplesIn, rateOut, rateIn)
	if !ok {
		return SampleStats{}, timeline.New(timeline.CatOverflow, "output sample count overflow")
	}
	durOut, ok := timeline.NsFromUnits(samplesOut, rateOut)
	if !ok {
		return SampleStats{}, timeline.New(timeline.CatOverflow, "output duration overflow")
	}
	errNs, ok := subCheck(durOut, durIn)
	if !ok {
		return SampleStats{}, timeline.New(timeline.CatOverflow, "duration error overflow")
	}
	return SampleStats{
		RateIn:          rateIn,
		RateOut:         rateOut,
		SamplesIn:       samplesIn,
		SamplesOut:      samplesOut,
		DurationInNs:    durIn,
		DurationOutNs:   durOut,
		DurationErrorNs: errNs,
	}, nil
}

// ratioRound returns round-half-up(a*b/c) via exact integer arithmetic and
// reports false when the result does not fit a non-negative int64.
func ratioRound(a, b, c int64) (int64, bool) {
	num := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	num.Mul(num, big.NewInt(2))
	num.Add(num, big.NewInt(c)) // 2ab + c
	quo := num.Quo(num, big.NewInt(2*c))
	if !quo.IsInt64() || quo.Sign() < 0 {
		return 0, false
	}
	return quo.Int64(), true
}

// ResampleF32 converts interleaved float32 PCM from rateIn to rateOut using
// linear interpolation. The channel interleave factor is preserved.
func ResampleF32(in []float32, rateIn, rateOut, channels int64) ([]float32, SampleStats, error) {
	if channels <= 0 || len(in)%int(channels) != 0 {
		return nil, SampleStats{}, timeline.New(timeline.CatConfig,
			"sample buffer is not an integer number of frames")
	}
	framesIn := int64(len(in)) / channels
	stats, err := Plan(rateIn, rateOut, framesIn)
	if err != nil {
		return nil, SampleStats{}, err
	}
	out := make([]float32, stats.SamplesOut*channels)
	for n := int64(0); n < stats.SamplesOut; n++ {
		pos := float64(n) * float64(rateIn) / float64(rateOut)
		base := math.Floor(pos)
		i0 := int64(base)
		frac := float32(pos - base)
		i1 := i0 + 1
		for ch := int64(0); ch < channels; ch++ {
			a := in[clampFrame(i0, framesIn)*channels+ch]
			b := in[clampFrame(i1, framesIn)*channels+ch]
			out[n*channels+ch] = a + (b-a)*frac
		}
	}
	return out, stats, nil
}

func clampFrame(i, n int64) int64 {
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

func subCheck(a, b int64) (int64, bool) {
	d := a - b
	// Signed subtraction overflow iff the result sign disagrees with both
	// operands' differing signs.
	if (a^b) < 0 && (d^a) < 0 {
		return 0, false
	}
	return d, true
}
