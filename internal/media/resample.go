package media

import (
	"errors"
	"fmt"
)

// ResampleConfig pins a deterministic linear resampler. The phase is always
// zero and arithmetic is fixed-precision, so identical input bytes always
// produce identical output bytes.
type ResampleConfig struct {
	InputRate  uint32
	OutputRate uint32
}

// Validate rejects zero rates and reports the exact integer ratio when the
// rates are commensurate.
func (c ResampleConfig) Validate() error {
	if c.InputRate == 0 || c.OutputRate == 0 {
		return fmt.Errorf("%w: sample rates must be non-zero", ErrSampleRate)
	}
	return nil
}

// Ratio returns the reduced (output, input) integer step ratio.
func (c ResampleConfig) Ratio() (outStep, inStep uint32) {
	g := gcd32(c.OutputRate, c.InputRate)
	return c.OutputRate / g, c.InputRate / g
}

// ErrSampleRate marks unsupported or invalid sample-rate configuration.
var ErrSampleRate = errors.New("sample rate configuration error")

func gcd32(a, b uint32) uint32 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// ResampleReport records input/output sample counts for the summary report.
type ResampleReport struct {
	InputSamples  int
	OutputSamples int
	InputRate     uint32
	OutputRate    uint32
}

// ResamplePCM converts mono or per-channel-interleaved float64 PCM using
// piecewise-linear interpolation with a fixed zero phase. Channel count is
// preserved; only the time axis changes.
func ResamplePCM(in []float64, channels int, cfg ResampleConfig) ([]float64, ResampleReport, error) {
	if err := cfg.Validate(); err != nil {
		return nil, ResampleReport{}, err
	}
	if channels <= 0 || len(in)%channels != 0 {
		return nil, ResampleReport{}, fmt.Errorf("%w: %d samples not divisible by %d channels",
			ErrSampleRate, len(in), channels)
	}
	inFrames := len(in) / channels
	if inFrames < 2 {
		// Nothing to interpolate against: pass through unchanged.
		out := append([]float64(nil), in...)
		return out, ResampleReport{inFrames, inFrames, cfg.InputRate, cfg.OutputRate}, nil
	}
	outStep, inStep := cfg.Ratio()
	// Output frame count: full input duration expressed at the output rate.
	outFrames := int((uint64(inFrames-1) * uint64(outStep) / uint64(inStep))) + 1
	out := make([]float64, outFrames*channels)
	for of := 0; of < outFrames; of++ {
		// Exact source position in input-frame units.
		posNum := uint64(of) * uint64(inStep)
		idx := posNum / uint64(outStep)
		fracNum := posNum % uint64(outStep)
		lo := idx
		hi := lo + 1
		if hi >= uint64(inFrames) {
			hi = uint64(inFrames) - 1
		}
		frac := float64(fracNum) / float64(outStep)
		for ch := 0; ch < channels; ch++ {
			a := in[lo*uint64(channels)+uint64(ch)]
			b := in[hi*uint64(channels)+uint64(ch)]
			out[of*channels+ch] = a + (b-a)*frac
		}
	}
	return out, ResampleReport{inFrames, outFrames, cfg.InputRate, cfg.OutputRate}, nil
}

// NormalizeAudio is the one-shot policy entry point: validate layout mapping,
// apply it, then resample. Both operations must be explicitly configured;
// mismatched layouts without a matrix are rejected, never guessed.
type AudioPlan struct {
	FromLayout Layout
	ToLayout   Layout
	Matrix     MappingMatrix
	Resample   ResampleConfig
}

// NormalizeAudio runs the plan and returns PCM plus a sample-count report.
func NormalizeAudio(in []float64, plan AudioPlan) ([]float64, ResampleReport, error) {
	if !plan.Matrix.From.Equal(plan.FromLayout) || !plan.Matrix.To.Equal(plan.ToLayout) {
		return nil, ResampleReport{}, fmt.Errorf("%w: matrix layout does not match plan", ErrLayoutMismatch)
	}
	mapped, err := plan.Matrix.ApplyPCM(in)
	if err != nil {
		return nil, ResampleReport{}, err
	}
	out, rep, err := ResamplePCM(mapped, len(plan.ToLayout.Channels), plan.Resample)
	if err != nil {
		return nil, ResampleReport{}, err
	}
	return out, rep, nil
}
