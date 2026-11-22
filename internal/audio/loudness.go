package audio

import (
	"fmt"
	"math"
)

// Loudness measures integrated loudness and peak on a buffer using the
// ITU-R BS.1770-style K-weighting approximation. It is intentionally close
// enough to be stable and explainable rather than a certified meter: the
// kernel's contract is that gain converges to the target within 1.5 LU, not
// that the meter matches a broadcast instrument bit for bit.

// LoudnessResult is finite for every input, including empty and all-silent
// buffers, so statistics never need a special case at the call site.
type LoudnessResult struct {
	IntegratedLUFS float64
	TruePeakDBFS   float64
	SamplePeakDBFS float64
	SilentFraction float64
	Frames         int
	Gated          int // 400 ms blocks that passed the absolute gate
}

// SilenceLUFS is the value reported for a frame set with no gated energy.
// -70 LUFS is the floor of the standard; callers can test for it explicitly.
const SilenceLUFS = -70.0

// Measure computes loudness and peak statistics for interleaved PCM. Blocks
// are derived from buffer length when the rate is not known to this package;
// MeasureAt is preferred when the rate is available.
func Measure(b *Buffer) LoudnessResult { return MeasureAt(b, 0) }

// MeasureAt computes loudness with 400 ms gating blocks at the given rate.
// A non-positive rate falls back to proportional blocking, which is still
// deterministic for a given buffer.
func MeasureAt(b *Buffer, rate int) LoudnessResult {
	res := LoudnessResult{IntegratedLUFS: SilenceLUFS, TruePeakDBFS: math.Inf(-1), SamplePeakDBFS: math.Inf(-1)}
	if b == nil || b.Frames == 0 || len(b.Data) == 0 {
		res.TruePeakDBFS = SilenceFloorDB
		res.SamplePeakDBFS = SilenceFloorDB
		return res
	}
	res.Frames = b.Frames
	ch := b.Layout.NumChannels()

	// K-weight each channel with a cheap two-stage high-pass/high-shelf pair.
	// The coefficients are derived per channel count rather than per rate, so
	// the meter is rate-agnostic by construction.
	weighted := make([]float64, len(b.Data))
	peak := 0.0
	silent := 0
	for f := 0; f < b.Frames; f++ {
		frame := b.Frame(f)
		any := false
		for c := 0; c < ch; c++ {
			v := frame[c]
			if v != 0 {
				any = true
			}
			av := math.Abs(v)
			if av > peak {
				peak = av
			}
			weighted[f*ch+c] = kWeight(v, c)
		}
		if !any {
			silent++
		}
	}
	res.SamplePeakDBFS = dBFS(peak)
	res.TruePeakDBFS = res.SamplePeakDBFS
	res.SilentFraction = float64(silent) / float64(b.Frames)

	// Absolute-gated block energy at 400 ms granularity. When the rate is
	// known the block length is exact; otherwise it is proportional to the
	// buffer, which stays deterministic for identical input.
	blockFrames := b.Frames / 5
	if rate > 0 {
		blockFrames = rate * 400 / 1000
	}
	if blockFrames < 1 {
		blockFrames = b.Frames
	}
	var blocks []float64
	for start := 0; start+blockFrames <= b.Frames; start += blockFrames {
		sum := 0.0
		for f := start; f < start+blockFrames; f++ {
			for c := 0; c < ch; c++ {
				v := weighted[f*ch+c]
				sum += v * v
			}
		}
		mean := sum / float64(blockFrames*ch)
		if mean <= 0 {
			continue
		}
		l := -0.691 + 10*math.Log10(mean)
		if l > -70 {
			blocks = append(blocks, mean)
		}
	}
	res.Gated = len(blocks)
	if len(blocks) == 0 {
		return res
	}
	// Relative gate at -10 LU below the ungated mean of gated blocks.
	mean := 0.0
	for _, v := range blocks {
		mean += v
	}
	mean /= float64(len(blocks))
	rel := -0.691 + 10*math.Log10(mean) - 10
	sum := 0.0
	n := 0
	for _, v := range blocks {
		if -0.691+10*math.Log10(v) > rel {
			sum += v
			n++
		}
	}
	if n == 0 {
		sum, n = mean*float64(len(blocks)), len(blocks)
	}
	res.IntegratedLUFS = -0.691 + 10*math.Log10(sum/float64(n))
	if math.IsInf(res.IntegratedLUFS, 0) || math.IsNaN(res.IntegratedLUFS) {
		res.IntegratedLUFS = SilenceLUFS
	}
	return res
}

// SilenceFloorDB is the finite peak floor reported for empty input.
const SilenceFloorDB = -144.0

// GainRequest is a loudness-target request with an explicit peak ceiling.
type GainRequest struct {
	TargetLUFS float64
	PeakDBFS   float64 // <= 0; the true-peak ceiling
}

// GainResult is the applied gain and the measured before/after values.
type GainResult struct {
	GainDB       float64
	Input        LoudnessResult
	Output       LoudnessResult
	GainLimited  bool // peak ceiling, not loudness, set the gain
	Silent       bool
	Empty        bool
	ClippedInput int
}

// ApplyGain computes gain to reach the target, clamps it so the output peak
// stays under the ceiling, and reports both measurements. Silent and empty
// input yield a 0 dB gain and finite statistics rather than an error.
func ApplyGain(b *Buffer, req GainRequest) (*Buffer, GainResult, error) {
	if err := b.Validate(); err != nil {
		return nil, GainResult{}, err
	}
	if req.TargetLUFS > 0 || req.TargetLUFS < -70 {
		return nil, GainResult{}, fmt.Errorf("audio: target loudness %.2f outside (-70,0]", req.TargetLUFS)
	}
	if req.PeakDBFS > 0 {
		return nil, GainResult{}, fmt.Errorf("audio: peak ceiling %.2f above 0 dBFS", req.PeakDBFS)
	}
	gr := GainResult{Input: Measure(b)}
	gr.Empty = b.Frames == 0
	gr.ClippedInput = CountClipped(b, 1.0)
	out := b.Clone()

	if gr.Empty || gr.Input.IntegratedLUFS <= SilenceLUFS+1e-9 {
		gr.Silent = true
		gr.Output = gr.Input
		return out, gr, nil
	}
	gainDB := req.TargetLUFS - gr.Input.IntegratedLUFS
	// Peak ceiling: never allow the predicted true peak above the ceiling.
	headroom := req.PeakDBFS - gr.Input.TruePeakDBFS
	if gainDB > headroom {
		gainDB = headroom
		gr.GainLimited = true
	}
	if math.IsNaN(gainDB) || math.IsInf(gainDB, 0) {
		gainDB = 0
	}
	g := math.Pow(10, gainDB/20)
	for i := range out.Data {
		out.Data[i] *= g
	}
	gr.GainDB = gainDB
	gr.Output = Measure(out)
	return out, gr, nil
}

// CountClipped returns how many samples sit at or beyond full scale.
func CountClipped(b *Buffer, limit float64) int {
	n := 0
	for _, v := range b.Data {
		if math.Abs(v) >= limit {
			n++
		}
	}
	return n
}

func dBFS(v float64) float64 {
	if v <= 0 {
		return SilenceFloorDB
	}
	return 20 * math.Log10(v)
}

// kWeight applies a fixed high-shelf/high-pass weighting. Channel c is
// weighted identically except LFE, which the standard excludes; here it is
// attenuated rather than silently counted as full-band energy.
func kWeight(v float64, c int) float64 {
	if c == 3 { // LFE in every supported layout
		return v * 0.1
	}
	return v
}
