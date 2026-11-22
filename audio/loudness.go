package audio

import (
	"math"

	"medpipe/timeline"
)

// SilenceLUFS is the reported loudness of an all-zero (or near-zero) input.
const SilenceLUFS = -70.0

// ceilingDBTP is the maximum true peak the limiter is allowed to emit.
const ceilingDBTP = -1.0

// LoudnessReport captures every loudness-stage number required by the
// manifest and CLI summary.
type LoudnessReport struct {
	InputLUFS        float64
	TargetLUFS       float64
	OutputLUFS       float64
	InputTruePeakDBTP  float64
	OutputTruePeakDBTP float64
	GainDB           float64
	ClippedSamples   int
}

// channelWeights implements the BS.1770 channel weighting. The LFE channel is
// excluded from loudness summation by definition.
func channelWeights(spec LayoutSpec) []float64 {
	w := make([]float64, spec.Channels)
	for i, label := range spec.Labels {
		switch label {
		case "Ls", "Rs", "Rls", "Rrs":
			w[i] = 1.41
		case "LFE":
			w[i] = 0
		default:
			w[i] = 1.0
		}
	}
	return w
}

// dcBlock approximates the BS.1770 RLB high-pass stage with a one-pole
// DC blocker, giving a deterministic K-weighting approximation without
// architecture-dependent transcendental filter coefficients.
func dcBlock(in []float64) []float64 {
	out := make([]float64, len(in))
	const r = 0.9949
	var x1, y1 float64
	for i, x := range in {
		y := x - x1 + r*y1
		out[i] = y
		x1, y1 = x, y
	}
	return out
}

// IntegratedLoudness measures gated BS.1770-style loudness in LUFS over
// interleaved samples. 400 ms blocks advance by 100 ms; both the absolute
// (-70 LUFS) and relative (-10 LU) gates are applied.
func IntegratedLoudness(samples []float64, channels int, layout string, rate uint32) (float64, error) {
	if channels <= 0 || len(samples)%channels != 0 {
		return 0, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageLoudness, "",
			"%d samples do not tile %d channels", len(samples), channels)
	}
	spec, err := LookupLayout(layout)
	if err != nil {
		return 0, err
	}
	if spec.Channels != channels {
		return 0, timeline.NewError(timeline.ErrCodeUnknownLayout, timeline.StageLoudness, "",
			"layout %q declares %d channels but block has %d", layout, spec.Channels, channels)
	}
	frames := len(samples) / channels
	blockLen := int(rate) * 4 / 10
	hop := int(rate) / 10
	if blockLen == 0 || frames < blockLen {
		// Short streams are measured as a single block instead of being
		// rejected: the gating math below handles a one-block window.
		blockLen = frames
		hop = frames
	}
	if blockLen == 0 {
		return SilenceLUFS, nil
	}
	weights := channelWeights(spec)
	filtered := make([][]float64, channels)
	for c := 0; c < channels; c++ {
		raw := make([]float64, frames)
		for f := 0; f < frames; f++ {
			raw[f] = samples[f*channels+c]
		}
		filtered[c] = dcBlock(raw)
	}
	var blockLoud []float64
	var blockMean []float64
	for start := 0; start+blockLen <= frames; start += hop {
		var sum float64
		for c := 0; c < channels; c++ {
			var ms float64
			for f := start; f < start+blockLen; f++ {
				v := filtered[c][f]
				ms += v * v
			}
			sum += weights[c] * ms / float64(blockLen)
		}
		blockMean = append(blockMean, sum)
		blockLoud = append(blockLoud, -0.691+10*math.Log10(sum+1e-12))
	}
	// Absolute gate at -70 LUFS.
	var passMean []float64
	for i, l := range blockLoud {
		if l >= SilenceLUFS {
			passMean = append(passMean, blockMean[i])
		}
	}
	if len(passMean) == 0 {
		return SilenceLUFS, nil
	}
	absLoud := func(means []float64) float64 {
		var s float64
		for _, m := range means {
			s += m
		}
		return -0.691 + 10*math.Log10(s/float64(len(means))+1e-12)
	}
	relThreshold := absLoud(passMean) - 10
	var gated []float64
	for i, l := range blockLoud {
		if l >= SilenceLUFS && l >= relThreshold {
			gated = append(gated, blockMean[i])
		}
	}
	if len(gated) == 0 {
		return SilenceLUFS, nil
	}
	return absLoud(gated), nil
}

// truePeak oversamples each channel 4x with linear interpolation and returns
// the maximum absolute sample, converted to dBTP.
func truePeak(samples []float64, channels int) float64 {
	var peak float64
	frames := len(samples) / channels
	for c := 0; c < channels; c++ {
		var prev float64
		for f := 0; f < frames; f++ {
			cur := samples[f*channels+c]
			for k := 0; k < 4; k++ {
				v := prev + (cur-prev)*float64(k)/4.0
				if a := math.Abs(v); a > peak {
					peak = a
				}
			}
			prev = cur
		}
	}
	if peak == 0 {
		return -96.0 // silence sentinel, still well below the -1 dBTP ceiling
	}
	return 20 * math.Log10(peak)
}

// LimitLoudness normalizes toward targetLUFS and hard-limits true peak to
// -1 dBTP. Silence passes through unchanged. The returned slice is a copy.
func LimitLoudness(samples []float64, channels int, layout string, rate uint32, targetLUFS float64) ([]float64, LoudnessReport, error) {
	inLUFS, err := IntegratedLoudness(samples, channels, layout, rate)
	if err != nil {
		return nil, LoudnessReport{}, err
	}
	inTP := truePeak(samples, channels)
	rep := LoudnessReport{
		InputLUFS: inLUFS, TargetLUFS: targetLUFS,
		InputTruePeakDBTP: inTP,
	}
	ceiling := math.Pow(10, ceilingDBTP/20)
	var gain float64
	if inLUFS <= SilenceLUFS {
		gain = 1 // silence must stay silence
	} else {
		gain = math.Pow(10, (targetLUFS-inLUFS)/20)
		// Never let the gain push the true peak past the ceiling.
		if inTP > -90 {
			maxGain := ceiling / math.Pow(10, inTP/20)
			if gain > maxGain {
				gain = maxGain
			}
		}
	}
	out := make([]float64, len(samples))
	for i, v := range samples {
		if math.Abs(v) > ceiling {
			rep.ClippedSamples++
		}
		out[i] = v * gain
	}
	// Hard clamp as a final guarantee against arithmetic overshoot.
	for i, v := range out {
		if v > ceiling {
			out[i] = ceiling
		} else if v < -ceiling {
			out[i] = -ceiling
		}
	}
	rep.GainDB = 20 * math.Log10(gain)
	rep.OutputLUFS, _ = IntegratedLoudness(out, channels, layout, rate)
	rep.OutputTruePeakDBTP = truePeak(out, channels)
	return out, rep, nil
}
