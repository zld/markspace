// Package audio implements the deterministic conversion stages: rational
// resampling with cross-block phase continuity, explicit channel-layout
// mapping with policy-driven missing/extra channel handling, and recomputable
// windowed loudness control with a true-peak limiter.
package audio

import (
	"mediatimeline/internal/clock"
)

// Resampler converts PCM frames between sample rates using linear
// interpolation on an exact rational step. The output phase is maintained on
// the *global* input timeline (never rebased per block), so streaming blocks
// concatenate bit-identically to one-shot processing of the whole stream.
type Resampler struct {
	inRate      uint32
	outRate     uint32
	totalInput  uint64    // known total input frames; 0 treats the final block as end
	phaseNum    uint64    // next output position on the global input timeline
	consumed    uint64    // total input frames presented so far
	sampleCount uint64    // total output frames emitted
	prevFrame   []float64 // last frame of the previous block, for boundary interp
	lastFrame   []float64
	channels    int
}

// NewResampler validates the rate pair. Both rates must be positive. When the
// total number of input frames is known (the common fixture/demo case), pass
// it so end-of-stream hold is distinguished from block-boundary interpolation;
// pass 0 for open-ended streams, in which case the final block holds.
func NewResampler(inRate, outRate, totalInput uint32) (*Resampler, error) {
	if inRate == 0 || outRate == 0 {
		return nil, clock.NewError(clock.ErrInvariant, "sample rates must be non-zero")
	}
	return &Resampler{inRate: inRate, outRate: outRate, totalInput: uint64(totalInput)}, nil
}

func (r *Resampler) den() uint64  { return uint64(r.outRate) }
func (r *Resampler) step() uint64 { return uint64(r.inRate) }

// RateStep returns the rational input-phase advance per output frame.
func (r *Resampler) RateStep() (num, den uint64) { return uint64(r.inRate), r.den() }

// Process resamples one interleaved-PCM block beginning at global input frame
// Consumed(). Samples are float64 in [-1,1]; channels is the frame width.
// Fractional output positions that straddle a block boundary are deferred and
// resolved when the next block arrives, using the previous block's final frame
// carried in prevFrame; this is what keeps streaming identical to one-shot.
func (r *Resampler) Process(in []float64, channels int) []float64 {
	if channels <= 0 || len(in) == 0 {
		return nil
	}
	inFrames := len(in) / channels
	r.channels = channels
	r.lastFrame = append(r.lastFrame[:0], in[len(in)-channels:]...)

	den, step := r.den(), r.step()
	start := r.consumed
	blockEnd := start + uint64(inFrames) // exclusive global input index
	endOfStream := r.totalInput == 0 || blockEnd >= r.totalInput
	capFrames := inFrames*int(r.outRate)/int(r.inRate) + 2
	out := make([]float64, 0, capFrames*channels)

	// 1. Resolve positions deferred from the previous block: their global
	// index is start-1 (the carried frame) and they interpolate toward the
	// first frame of this block.
	for r.phaseNum/den < start {
		frac := r.phaseNum % den
		for c := 0; c < channels; c++ {
			cur := r.prevFrame[c]
			nxt := in[c]
			out = append(out, cur+(nxt-cur)*float64(frac)/float64(den))
		}
		r.phaseNum += step
	}

	// 2. Emit positions whose primary index lies inside this block. A
	// fractional position needing the next block's first sample is deferred,
	// except at the true end of the stream where last-sample hold applies.
	for {
		gidx := r.phaseNum / den
		if gidx >= blockEnd {
			break
		}
		frac := r.phaseNum % den
		local := int(gidx - start)
		if frac != 0 && local+1 >= inFrames && !endOfStream {
			break // defer to the next block
		}
		for c := 0; c < channels; c++ {
			cur := in[local*channels+c]
			if frac == 0 || local+1 >= inFrames {
				out = append(out, cur) // exact sample or terminal hold
				continue
			}
			nxt := in[(local+1)*channels+c]
			out = append(out, cur+(nxt-cur)*float64(frac)/float64(den))
		}
		r.phaseNum += step
	}

	r.prevFrame = append(r.prevFrame[:0], r.lastFrame...)
	r.consumed = blockEnd
	r.sampleCount += uint64(len(out) / channels)
	return out
}

// FramesEmitted reports cumulative output frames across all blocks.
func (r *Resampler) FramesEmitted() uint64 { return r.sampleCount }

// Flush finalizes end-of-stream. The carry-frame model already emits every
// in-range position during the final Process call, so Flush only adds a
// terminal hold-frame when a residual remains past the last presented sample,
// ensuring full-duration coverage without shortening the stream.
func (r *Resampler) Flush() []float64 {
	endPos := r.consumed * r.den()
	if r.sampleCount == 0 || len(r.lastFrame) != r.channels || r.phaseNum <= endPos {
		return nil
	}
	out := append([]float64(nil), r.lastFrame...)
	r.sampleCount++
	return out
}

// Phase returns the global phase numerator and its denominator for checkpoints.
func (r *Resampler) Phase() (num, den uint64) { return r.phaseNum, r.den() }

// Consumed reports total input frames presented across blocks.
func (r *Resampler) Consumed() uint64 { return r.consumed }

// TotalInput reports the known total input frame count (0 when open-ended).
func (r *Resampler) TotalInput() uint64 { return r.totalInput }

// SetTotalInput declares the total input frame count. The pipeline calls it
// once the container's sample table is known, so interior block boundaries
// interpolate instead of applying end-of-stream hold.
func (r *Resampler) SetTotalInput(n uint64) { r.totalInput = n }

// PrevFrame returns the carried last frame for checkpointing.
func (r *Resampler) PrevFrame() []float64 { return append([]float64(nil), r.prevFrame...) }

// RestorePhase rehydrates phase, counters and carried frame from a checkpoint.
func (r *Resampler) RestorePhase(phaseNum, phaseDen, consumed, frames uint64, prev []float64) {
	r.phaseNum, r.consumed, r.sampleCount = phaseNum, consumed, frames
	if phaseDen != 0 {
		r.outRate = uint32(phaseDen)
	}
	r.prevFrame = append(r.prevFrame[:0], prev...)
}
