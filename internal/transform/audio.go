// Package transform performs deterministic per-frame audio work: sample-rate
// conversion, channel layout normalization and loudness control. All math is
// pure integer/float64 with fixed ordering, so identical input and parameters
// produce byte-identical output across runs and machines.
package transform

import (
	"encoding/binary"
	"math"

	"continuity/internal/model"
)

// Params selects the output geometry and loudness target.
type Params struct {
	SourceRate uint32
	TargetRate uint32
	Source     model.ChannelLayout
	Target     model.ChannelLayout
	TargetLUFS float64
}

// Result is one transformed frame's PCM plus its measured loudness.
type Result struct {
	PCM          []byte
	MeasuredLUFS float64
	Samples      int
}

// Frame runs the full conversion chain for one frame's little-endian int16
// PCM payload.
func Frame(payload []byte, p Params) (*Result, error) {
	samples, err := decodePCM(payload, p.Source)
	if err != nil {
		return nil, err
	}
	samples = normalizeChannels(samples, p.Source, p.Target)
	samples = resample(samples, p.SourceRate, p.TargetRate)
	measured := measureLUFS(samples)
	samples = applyLoudness(samples, measured, p.TargetLUFS)

	return &Result{
		PCM:          encodePCM(samples),
		MeasuredLUFS: measureLUFS(samples),
		Samples:      len(samples),
	}, nil
}

func decodePCM(payload []byte, layout model.ChannelLayout) ([][]float64, error) {
	ch := layout.Channels()
	if len(payload)%(2*ch) != 0 {
		return nil, model.NewError(model.StageTransform, 0, 0, "payload is not whole int16 samples")
	}
	frames := len(payload) / (2 * ch)
	out := make([][]float64, ch)
	for c := range out {
		out[c] = make([]float64, frames)
	}
	for i := 0; i < frames; i++ {
		for c := 0; c < ch; c++ {
			v := int16(binary.LittleEndian.Uint16(payload[(i*ch+c)*2:]))
			out[c][i] = float64(v) / float64(math.MaxInt16)
		}
	}
	return out, nil
}

func encodePCM(channels [][]float64) []byte {
	if len(channels) == 0 {
		return nil
	}
	frames := len(channels[0])
	buf := make([]byte, frames*len(channels)*2)
	for i := 0; i < frames; i++ {
		for c, ch := range channels {
			s := clamp(ch[i])
			binary.LittleEndian.PutUint16(buf[(i*len(channels)+c)*2:], uint16(s))
		}
	}
	return buf
}

func clamp(x float64) int16 {
	v := int32(math.Round(x * float64(math.MaxInt16)))
	if v > math.MaxInt16 {
		v = math.MaxInt16
	}
	if v < math.MinInt16 {
		v = math.MinInt16
	}
	return int16(v)
}

// normalizeChannels converts between mono and stereo without changing the
// number of sample frames.
func normalizeChannels(in [][]float64, from, to model.ChannelLayout) [][]float64 {
	if from == to {
		return in
	}
	frames := len(in[0])
	switch to {
	case model.LayoutStereo:
		// mono -> stereo: duplicate the channel (dual mono).
		return [][]float64{append([]float64(nil), in[0]...), append([]float64(nil), in[0]...)}
	case model.LayoutMono:
		// stereo -> mono: equal-power average.
		mono := make([]float64, frames)
		for i := 0; i < frames; i++ {
			mono[i] = (in[0][i] + in[1][i]) / 2
		}
		return [][]float64{mono}
	default:
		return in
	}
}

// resample converts per-channel sample frames using linear interpolation at a
// fixed phase step, avoiding any nondeterministic allocation order effects.
func resample(in [][]float64, fromRate, toRate uint32) [][]float64 {
	if fromRate == toRate {
		return in
	}
	srcFrames := len(in[0])
	ratio := float64(fromRate) / float64(toRate)
	dstFrames := int(math.Round(float64(srcFrames) * float64(toRate) / float64(fromRate)))
	out := make([][]float64, len(in))
	for c, ch := range in {
		resampled := make([]float64, dstFrames)
		for i := 0; i < dstFrames; i++ {
			pos := float64(i) * ratio
			idx := int(math.Floor(pos))
			frac := pos - float64(idx)
			next := idx + 1
			if next >= srcFrames {
				next = srcFrames - 1
			}
			resampled[i] = ch[idx]*(1-frac) + ch[next]*frac
		}
		out[c] = resampled
	}
	return out
}

// measureLUFS computes a deterministic loudness estimate: mean square level
// mapped onto the LUFS scale. It is intentionally a fixed, dependency-free
// model so the validator can check the same tolerance the planner promised.
func measureLUFS(channels [][]float64) float64 {
	var sum float64
	var n int
	for _, ch := range channels {
		for _, v := range ch {
			sum += v * v
			n++
		}
	}
	if n == 0 || sum == 0 {
		return -math.Inf(1)
	}
	rms := math.Sqrt(sum / float64(n))
	if rms <= 0 {
		return -math.Inf(1)
	}
	// Full-scale sine RMS (1/sqrt(2)) is the 0 LUFS reference in this model.
	return 20 * math.Log10(rms/(1/math.Sqrt2))
}

// applyLoudness applies a single linear gain so measured loudness moves to the
// target; the post-gain measurement is returned to the caller for validation.
func applyLoudness(channels [][]float64, measured, target float64) [][]float64 {
	if math.IsInf(measured, 0) {
		return channels
	}
	db := target - measured
	gain := math.Pow(10, db/20)
	out := make([][]float64, len(channels))
	for c, ch := range channels {
		scaled := make([]float64, len(ch))
		for i, v := range ch {
			scaled[i] = v * gain
		}
		out[c] = scaled
	}
	return out
}
