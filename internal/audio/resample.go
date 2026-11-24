package audio

import (
	"fmt"
	"math"

	"mediapipe/internal/model"
)

// Supported source and target rates. Every ratio between these rates is
// rational with small L/M factors, so conversion needs no floating timebase.
var supportedRates = map[int64]bool{44100: true, 48000: true, 96000: true}

// RationalRatio reduces target/source to coprime up/down factors. Upsampling
// by L then decimating by M yields exactly target samples per source second.
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

// filterTaps is the fixed FIR window length. Determinism requires the same
// taps on every run, so the low-pass is a windowed sinc generated once with
// a hardcoded cutoff derived from the active ratio.
const filterTaps = 16

// buildFilter creates a normalized windowed-sinc low-pass with cutoff at
// min(1, up/down) of the upsampled Nyquist band. Taps are symmetric so the
// group delay is constant and repeat runs produce bit-identical coefficients.
func buildFilter(up, down int64) []float64 {
	cutoff := 1.0
	if up < down {
		cutoff = float64(up) / float64(down)
	}
	f := make([]float64, filterTaps)
	mid := (filterTaps - 1) / 2.0
	var sum float64
	for i := range f {
		x := float64(i) - mid
		var sinc float64
		if math.Abs(x) < 1e-12 {
			sinc = cutoff
		} else {
			w := math.Pi * cutoff * x
			sinc = math.Sin(w) / (math.Pi * x)
		}
		// Hann window.
		win := 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/(filterTaps-1)))
		f[i] = sinc * win
		sum += f[i]
	}
	for i := range f {
		f[i] /= sum
	}
	return f
}

// ResampleResult reports the converted stream plus boundary bookkeeping.
type ResampleResult struct {
	Samples    []float64
	Rate       int64
	ZeroPads   int // input samples synthesized as zeros at the boundaries
	WindowSkips int // output points lacking a full filter window
}

// Resample converts one mono channel deterministically. The input is
// zero-stuffed by up, low-passed with the fixed FIR and decimated by down.
// When fewer than one filter window of input is available at a boundary, the
// missing taps are deterministically treated as zeros and counted.
func Resample(input []float64, sourceRate, targetRate int64) (*ResampleResult, error) {
	up, down, err := RationalRatio(sourceRate, targetRate)
	if err != nil {
		return nil, err
	}
	if up == 1 && down == 1 {
		return &ResampleResult{Samples: append([]float64(nil), input...), Rate: targetRate}, nil
	}
	filter := buildFilter(up, down)
	// Output count rounded half-up from the rational ratio.
	nOut := (int64(len(input))*up + down/2) / down
	out := make([]float64, 0, nOut)
	pads, skips := 0, 0
	for k := int64(0); k < nOut; k++ {
		// Index into the zero-stuffed stream this output taps from.
		stuffed := k * down
		base := stuffed / up
		offset := int(stuffed % up)
		var acc float64
		full := true
		for j, coeff := range filter {
			// FIR reads input positions base+j-mid around the center tap.
			idx := int(base) + j - filterTaps/2
			var v float64
			switch {
			case idx < 0 || idx >= len(input):
				v = 0
				pads++
				full = false
			default:
				v = input[idx]
			}
			// Polyphase selection: only taps aligned with the stuffing phase.
			if (j+offset)%int(up) == 0 {
				acc += coeff * v * float64(up)
			}
		}
		if !full {
			skips++
		}
		out = append(out, acc)
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
