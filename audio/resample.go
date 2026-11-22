// Package audio performs stream-safe rate conversion, channel layout
// normalisation and loudness control. Every numeric path is either fixed
// point or uses one documented rounding rule so chunked and one-shot
// processing produce bit-identical, reproducible output.
package audio

import (
	"errors"
)

// Resampler converts PCM frames between sample rates using fixed-point
// linear interpolation. All arithmetic is integer: positions are a rational
// fraction num/den, and each interpolated sample is rounded half away from
// zero. Because each output is a pure function of two input samples and the
// rational position, results are identical regardless of chunk boundaries.
type Resampler struct {
	inRate, outRate int
	num, den        int64 // input-position step per output sample, num/den
	pos             int64 // fractional input position, in units of 1/den
	lastSample      int32 // last input sample, retained across chunks
	haveLast        bool
	inDone          int64 // total input frames consumed over the stream
	outDone         int64 // total output frames produced
}

// NewResampler builds a streaming converter. Both rates must be positive.
func NewResampler(inRate, outRate int) (*Resampler, error) {
	if inRate <= 0 || outRate <= 0 {
		return nil, errors.New("audio: sample rates must be positive")
	}
	g := gcd(int64(inRate), int64(outRate))
	return &Resampler{
		inRate: inRate, outRate: outRate,
		num: int64(inRate) / g, den: int64(outRate) / g,
	}, nil
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// roundHalfAway interpolates between a and b with weight w/den, rounding half
// away from zero.
func interpolate(a, b int32, w, den int64) int32 {
	v := int64(a)*(den-w) + int64(b)*w
	if v >= 0 {
		v += den / 2
	} else {
		v -= den / 2
	}
	return int32(v / den)
}

// Process converts one chunk of mono PCM16 frames. It may retain the final
// input sample internally to interpolate the next chunk, so callers must keep
// feeding chunks through the same instance and call Flush at end of stream.
//
// `pos` is the global fractional input position of the next output sample,
// measured in units of 1/den. Each output is a pure function of two adjacent
// input samples and the rational position, so chunk boundaries cannot change
// any produced value.
func (r *Resampler) Process(in []int16) []int16 {
	if len(in) == 0 {
		return nil
	}
	base := r.inDone // global index of in[0]
	var out []int16
	for {
		whole := r.pos / r.den
		frac := r.pos % r.den
		// Need two input samples whole and whole+1. The second may be the
		// retained sample carried from the previous chunk.
		if whole+1 > r.inDone+int64(len(in))-1 {
			break
		}
		a, okA := r.sampleAt(whole, base, in)
		b, okB := r.sampleAt(whole+1, base, in)
		if !okA || !okB {
			break
		}
		if frac == 0 {
			out = append(out, int16(a))
		} else {
			out = append(out, int16(interpolate(a, b, frac, r.den)))
		}
		r.outDone++
		r.pos += r.num
	}

	r.inDone += int64(len(in))
	r.lastSample = int32(in[len(in)-1])
	r.haveLast = true
	return out
}

// sampleAt resolves a global input index against the retained previous sample
// and the current chunk.
func (r *Resampler) sampleAt(global, base int64, in []int16) (int32, bool) {
	if global == base-1 {
		return r.lastSample, r.haveLast
	}
	local := global - base
	if local < 0 || local >= int64(len(in)) {
		return 0, false
	}
	return int32(in[local]), true
}

// Flush emits any final output sample that lands exactly on the last input
// frame once the stream is complete.
func (r *Resampler) Flush() []int16 {
	if !r.haveLast {
		return nil
	}
	var out []int16
	whole := r.pos / r.den
	frac := r.pos % r.den
	if frac == 0 && whole == r.inDone-1 {
		out = append(out, int16(r.lastSample))
		r.outDone++
		r.pos += r.num
	}
	return out
}

// InputFrames reports total input frames consumed.
func (r *Resampler) InputFrames() int64 { return r.inDone }

// OutputFrames reports total output frames produced.
func (r *Resampler) OutputFrames() int64 { return r.outDone }

// ResamplerState is the serialisable cross-chunk checkpoint.
type ResamplerState struct {
	InRate     int   `json:"in_rate"`
	OutRate    int   `json:"out_rate"`
	Num        int64 `json:"num"`
	Den        int64 `json:"den"`
	Pos        int64 `json:"pos"`
	LastSample int32 `json:"last_sample"`
	HaveLast   bool  `json:"have_last"`
	InDone     int64 `json:"in_done"`
	OutDone    int64 `json:"out_done"`
}

// Snapshot captures resampler state for checkpoint/resume.
func (r *Resampler) Snapshot() ResamplerState {
	return ResamplerState{
		InRate: r.inRate, OutRate: r.outRate, Num: r.num, Den: r.den,
		Pos: r.pos, LastSample: r.lastSample, HaveLast: r.haveLast,
		InDone: r.inDone, OutDone: r.outDone,
	}
}

// RestoreResampler rebuilds a resampler from a checkpoint.
func RestoreResampler(s ResamplerState) *Resampler {
	return &Resampler{
		inRate: s.InRate, outRate: s.OutRate, num: s.Num, den: s.Den, pos: s.Pos,
		lastSample: s.LastSample, haveLast: s.HaveLast,
		inDone: s.InDone, outDone: s.OutDone,
	}
}

// ResampleAll is a convenience one-shot wrapper used to prove chunked and
// one-shot processing agree exactly.
func ResampleAll(in []int16, inRate, outRate int) ([]int16, error) {
	r, err := NewResampler(inRate, outRate)
	if err != nil {
		return nil, err
	}
	out := r.Process(in)
	out = append(out, r.Flush()...)
	return out, nil
}
