package audio

import (
	"math"

	"medkernel/media"
)

// Supported sample rates cover the rates used by self-generated fixtures.
var supportedRates = map[int]bool{
	8000: true, 16000: true, 22050: true, 24000: true,
	32000: true, 44100: true, 48000: true, 96000: true,
}

// Resampler converts between sample rates with linear interpolation. It is
// streaming: at most one source frame (one sample per channel) and one phase
// value are carried between Process calls, so memory use stays bounded
// regardless of stream length.
type Resampler struct {
	SrcRate int
	DstRate int
	step    float64 // source frames consumed per output frame
	phase   float64 // next output position relative to the carried frame
	carry   []float32
	have    bool
	out     int64 // total output frames produced
}

// NewResampler validates both rates and builds the resampler.
func NewResampler(srcRate, dstRate int) (*Resampler, error) {
	if !supportedRates[srcRate] || !supportedRates[dstRate] {
		return nil, media.New(media.ClassAudio, "audio.resample",
			"unsupported sample rate")
	}
	return &Resampler{
		SrcRate: srcRate, DstRate: dstRate,
		step: float64(srcRate) / float64(dstRate),
	}, nil
}

// Process resamples one interleaved frame block, preserving its PTS.
func (r *Resampler) Process(in Frame) (Frame, error) {
	if in.Rate != r.SrcRate {
		return Frame{}, media.New(media.ClassAudio, "audio.resample",
			"frame rate does not match resampler").WithIndex(int64(in.Rate))
	}
	src := in.Samples
	ch := in.Channels
	if r.have {
		joined := make([]float32, 0, len(r.carry)+len(src))
		joined = append(joined, r.carry...)
		joined = append(joined, src...)
		src = joined
	}
	nFrames := len(src) / ch
	var out []float32
	p := r.phase
	for {
		idx := int(math.Floor(p))
		if idx+1 >= nFrames {
			break
		}
		frac := float32(p - float64(idx))
		baseA, baseB := idx*ch, (idx+1)*ch
		for c := 0; c < ch; c++ {
			a, b := src[baseA+c], src[baseB+c]
			out = append(out, a+(b-a)*frac)
		}
		p += r.step
	}
	// Carry the final source frame and rebase the phase onto it.
	r.carry = append(r.carry[:0], src[(nFrames-1)*ch:]...)
	r.have = true
	r.phase = p - float64(nFrames-1)
	r.out += int64(len(out) / ch)
	return Frame{
		Samples: out, Channels: ch, Rate: r.DstRate, PTS90: in.PTS90,
	}, nil
}

// OutputFrames reports how many output frames have been emitted in total.
func (r *Resampler) OutputFrames() int64 { return r.out }

// Reset clears all carried state (used at PSI track-map boundaries).
func (r *Resampler) Reset() {
	r.phase, r.have, r.out, r.carry = 0, false, 0, r.carry[:0]
}
