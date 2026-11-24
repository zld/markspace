package audio

import (
	"fmt"
	"math"

	"mediapipe/internal/model"
)

// Supported source and target rates. Every ratio between these rates is
// rational, but the resampler below works for any ratio because it maps each
// output sample to a fractional input position rather than a fixed phase set.
var supportedRates = map[int64]bool{44100: true, 48000: true, 96000: true}

// RationalRatio reduces target/source to coprime up/down factors. Upsampling
// by up then decimating by down yields exactly target samples per source
// second. The factors are also used for deterministic output-length rounding.
func RationalRatio(sourceRate, targetRate int64) (up, down int64, err error) {
	if !supportedRates[sourceRate] || !supportedRates[targetRate] {
		return 0, 0, model.NewError(model.ErrInvalidConfig, "", 0, 0,
			fmt.Sprintf("unsupported rate pair %d->%d", sourceRate, targetRate))
	}
	g := gcd(targetRate, sourceRate)
	return targetRate / g, sourceRate / g, nil
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// filterTaps is the fixed FIR window length. The same number of taps is used
// for every ratio so reruns are bit-identical and memory stays bounded by a
// single window rather than the total sample count.
const filterTaps = 16

// ResampleResult reports the converted stream plus boundary bookkeeping.
type ResampleResult struct {
	Samples     []float64
	Rate        int64
	ZeroPads    int // input taps synthesized as zeros at the boundaries
	WindowSkips int // output points lacking a complete in-range filter window
}

// lowpassSinc is one windowed-sinc coefficient at signed distance d from the
// fractional sample position. cutoff is 1.0 for upsampling and the Nyquist
// ratio target/source for downsampling, providing anti-alias protection.
func lowpassSinc(d, cutoff float64) float64 {
	if math.Abs(d) < 1e-12 {
		return cutoff
	}
	w := math.Pi * cutoff * d
	return math.Sin(w) / (math.Pi * d)
}

// Resample converts one mono channel deterministically. Each output sample is
// placed at a fractional input position and formed by a fixed 16-tap Hann-
// windowed sinc FIR. When the window extends past the input, the missing taps
// are deterministically treated as zeros, counted in ZeroPads, and the output
// is marked in WindowSkips. Coefficients are renormalized per output so a
// constant signal is preserved exactly regardless of fractional delay.
func Resample(input []float64, sourceRate, targetRate int64) (*ResampleResult, error) {
	if _, _, err := RationalRatio(sourceRate, targetRate); err != nil {
		return nil, err
	}
	if sourceRate == targetRate {
		return &ResampleResult{Samples: append([]float64(nil), input...), Rate: targetRate}, nil
	}
	n := len(input)
	// Half-up rounding of the rational output length.
	nOut := int((int64(n)*targetRate + sourceRate/2) / sourceRate)
	out := make([]float64, 0, nOut)
	ratio := float64(sourceRate) / float64(targetRate)
	cutoff := 1.0
	if targetRate < sourceRate {
		cutoff = float64(targetRate) / float64(sourceRate)
	}
	half := filterTaps / 2
	last := float64(filterTaps - 1)
	pads, skips := 0, 0

	for k := 0; k < nOut; k++ {
		pos := float64(k) * ratio
		center := math.Floor(pos)
		base := int(center) - half + 1 // first input index covered by the window
		var acc, coeffSum float64
		incomplete := false
		for i := 0; i < filterTaps; i++ {
			idx := base + i
			d := float64(idx) - pos
			hann := 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/last))
			coeff := lowpassSinc(d, cutoff) * hann
			coeffSum += coeff
			if idx < 0 || idx >= n {
				pads++
				incomplete = true
				continue // zero-padded tap contributes nothing
			}
			acc += coeff * input[idx]
		}
		if incomplete {
			skips++
		}
		out = append(out, acc/coeffSum)
	}
	return &ResampleResult{Samples: out, Rate: targetRate, ZeroPads: pads, WindowSkips: skips}, nil
}

// ResampleStereo applies Resample independently to L and R so both channels
// share identical boundary accounting.
func ResampleStereo(interleaved []float64, sourceRate, targetRate int64) (*ResampleResult, error) {
	if len(interleaved)%2 != 0 {
		return nil, model.NewError(model.ErrInvalidConfig, "", 0, 0,
			"stereo PCM length must be even")
	}
	l := make([]float64, 0, len(interleaved)/2)
	r := make([]float64, 0, len(interleaved)/2)
	for i := 0; i < len(interleaved); i += 2 {
		l = append(l, interleaved[i])
		r = append(r, interleaved[i+1])
	}
	rl, err := Resample(l, sourceRate, targetRate)
	if err != nil {
		return nil, err
	}
	rr, err := Resample(r, sourceRate, targetRate)
	if err != nil {
		return nil, err
	}
	if len(rl.Samples) != len(rr.Samples) || rl.ZeroPads != rr.ZeroPads {
		return nil, model.NewError(model.ErrIO, "", 0, 0, "channel resample divergence")
	}
	out := make([]float64, 0, len(rl.Samples)*2)
	for i := range rl.Samples {
		out = append(out, rl.Samples[i], rr.Samples[i])
	}
	return &ResampleResult{Samples: out, Rate: targetRate, ZeroPads: rl.ZeroPads + rr.ZeroPads,
		WindowSkips: rl.WindowSkips + rr.WindowSkips}, nil
}
