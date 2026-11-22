package audio

import (
	"medpipe/timeline"
)

// Resampler converts a multi-channel stream from one integer sample rate to
// another while preserving filter state across independently submitted
// blocks. Positions are tracked as integer input-sample indices plus a
// rational phase, so no floating point time accumulator can drift.
type Resampler struct {
	inRate      int
	outRate     int
	channels    int
	inputBase   int64  // absolute input index of the first block-0 sample
	consumed    int64  // absolute input samples already referenced
	produced    int64  // absolute output samples already emitted
	tail        []float64 // last input frame of the previous block, per channel
	haveTail    bool
	flushed     bool
}

// NewResampler builds a stateful resampler. Rates must be positive and the
// channel count non-zero.
func NewResampler(inRate, outRate uint32, channels int) (*Resampler, error) {
	if inRate == 0 || outRate == 0 {
		return nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageResample, "",
			"resample rates must be non-zero")
	}
	if channels <= 0 {
		return nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageResample, "",
			"channel count must be positive")
	}
	return &Resampler{
		inRate: int(inRate), outRate: int(outRate), channels: channels,
	}, nil
}

// ExpectedOutputs is the deterministic theoretical output frame count for an
// input of n frames, using nearest rounding of n*out/in.
func (r *Resampler) ExpectedOutputs(inputFrames int64) int64 {
	return (inputFrames*int64(r.outRate) + int64(r.inRate)/2) / int64(r.inRate)
}

// interpolateAt reads one output channel at absolute input index k using
// linear interpolation. Values before the first or after the last input
// sample resolve to zero, which is the fixed end-block padding policy.
func (r *Resampler) interpolateAt(block [][]float64, ch int, k int64) float64 {
	rel := k - r.inputBase
	n := int64(len(block[ch]))
	switch {
	case rel < 0:
		if !r.haveTail || rel < -1 {
			return 0
		}
		// The only legal negative index is the previous block's last frame.
		return r.tail[ch]
	case rel >= n:
		return 0 // fixed zero-padding tail policy
	}
	i0 := int(rel)
	x0 := block[ch][i0]
	if int64(i0)+1 >= n {
		return x0
	}
	frac := r.phaseFrac(k)
	return x0 + (block[ch][i0+1]-x0)*frac
}

// phaseFrac returns the sub-sample position of output k inside input frame
// [k_floor, k_floor+1), computed in exact rational arithmetic.
func (r *Resampler) phaseFrac(k int64) float64 {
	num := k*int64(r.inRate) - r.consumed*int64(r.outRate)
	// Note: r.consumed at call time equals floor(k*in/out); numerator is in
	// [0, outRate). Keep the rational as float64 only for the final weight.
	if num <= 0 {
		return 0
	}
	return float64(num) / float64(r.outRate)
}

// Process converts one block of per-channel frames. The block's frame count
// must be identical on every channel. State from prior blocks is reused.
func (r *Resampler) Process(block [][]float64) ([][]float64, error) {
	if r.flushed {
		return nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageResample, "",
			"resampler already flushed")
	}
	if len(block) != r.channels {
		return nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageResample, "",
			"block has %d channels, resampler configured for %d", len(block), r.channels)
	}
	n := len(block[0])
	for c := 1; c < r.channels; c++ {
		if len(block[c]) != n {
			return nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageResample, "",
				"channel %d has %d frames, channel 0 has %d", c, len(block[c]), n)
		}
	}
	if n == 0 {
		return make([][]float64, r.channels), nil
	}
	totalIn := r.inputBase + int64(n)
	need := r.ExpectedOutputs(totalIn)
	outN := int(need - r.produced)
	if outN < 0 {
		outN = 0
	}
	out := make([][]float64, r.channels)
	for c := range out {
		out[c] = make([]float64, outN)
	}
	for j := 0; j < outN; j++ {
		kAbs := r.produced + int64(j)
		// floor of the rational input position
		inPos := (kAbs*int64(r.inRate) + int64(r.outRate)/2) / int64(r.outRate)
		r.consumed = inPos
		for c := 0; c < r.channels; c++ {
			out[c][j] = r.interpolateAt(block, c, inPos)
		}
	}
	r.produced += int64(outN)
	// Retain the final frame so the next block can interpolate across the
	// boundary; update the absolute base to the start of the following block.
	r.tail = make([]float64, r.channels)
	for c := 0; c < r.channels; c++ {
		r.tail[c] = block[c][n-1]
	}
	r.haveTail = true
	r.inputBase = totalIn
	return out, nil
}

// Flush emits any residual output owed against the zero-padded tail. After
// Flush the resampler rejects further Process calls.
func (r *Resampler) Flush() [][]float64 {
	r.flushed = true
	need := r.ExpectedOutputs(r.inputBase)
	outN := int(need - r.produced)
	if outN <= 0 {
		return make([][]float64, r.channels)
	}
	out := make([][]float64, r.channels)
	for c := range out {
		out[c] = make([]float64, outN)
	}
	empty := make([][]float64, r.channels) // all reads past end resolve to zero
	for j := 0; j < outN; j++ {
		kAbs := r.produced + int64(j)
		inPos := (kAbs*int64(r.inRate) + int64(r.outRate)/2) / int64(r.outRate)
		for c := 0; c < r.channels; c++ {
			out[c][j] = r.interpolateAt(empty, c, inPos)
		}
	}
	r.produced += int64(outN)
	return out
}

// Produced reports the total output frames emitted so far.
func (r *Resampler) Produced() int64 { return r.produced }

// Consumed reports the highest absolute input index referenced.
func (r *Resampler) Consumed() int64 { return r.consumed }
