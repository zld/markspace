package audio

import (
	"audionorm/internal/media"
)

// Resampler converts mono or interleaved multi-channel PCM from an
// arbitrary source rate to the unified 48 kHz clock using linear
// interpolation. A fractional position accumulator is carried across
// calls so chunked streaming never accumulates rounding drift.
type Resampler struct {
	channels int
	step     uint64 // source-position increment per output sample (1<<32 fixed point)
	pos      uint64 // current position relative to the carried frame
	carry    []float32
}

const resampleFracBits = 32

// NewResampler builds a resampler for the given source rate and channel count.
func NewResampler(srcRate uint32, channels int) *Resampler {
	return &Resampler{
		channels: channels,
		step:     uint64(uint64(srcRate) << resampleFracBits / media.TimelineRate),
	}
}

// Reset returns the resampler to its initial phase.
func (r *Resampler) Reset() {
	r.pos = 0
	r.carry = nil
}

// Process converts one PCM chunk. The returned slice keeps the input's
// channel layout; the sub-sample phase is retained for the next call.
func (r *Resampler) Process(in []float32) []float32 {
	if r.channels <= 0 || len(in) == 0 {
		return nil
	}
	// Window = carried final frame of the previous chunk + new frames.
	src := in
	if r.carry != nil {
		src = append(append([]float32(nil), r.carry...), in...)
	}
	totalFrames := len(src) / r.channels
	var out []float32
	for {
		idx := int(r.pos >> resampleFracBits)
		if idx+1 >= totalFrames {
			break // need the next chunk to interpolate beyond this frame
		}
		frac := r.pos & (1<<resampleFracBits - 1)
		g := float32(frac) / float32(1<<resampleFracBits)
		for c := 0; c < r.channels; c++ {
			a := src[idx*r.channels+c]
			b := src[(idx+1)*r.channels+c]
			out = append(out, a+(b-a)*g)
		}
		r.pos += r.step
	}
	// Carry the last frame and rebase the position onto it.
	if totalFrames > 0 {
		r.carry = append([]float32(nil), src[(totalFrames-1)*r.channels:]...)
		r.pos -= uint64(totalFrames-1) << resampleFracBits
	}
	return out
}

// Flush emits any remaining output supported solely by the carried
// frame (held value, no extrapolation beyond it).
func (r *Resampler) Flush() []float32 {
	if r.carry == nil {
		return nil
	}
	out := append([]float32(nil), r.carry...)
	r.carry = nil
	r.pos = 0
	return out
}

// ResampleMono is a convenience wrapper for one-shot mono conversion.
func ResampleMono(in []float32, srcRate uint32) []float32 {
	r := NewResampler(srcRate, 1)
	out := r.Process(in)
	return append(out, r.Flush()...)
}
