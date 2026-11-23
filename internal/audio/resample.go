package audio

import (
	"math"

	"mediaforge/internal/clock"
)

// commonRates lists the sample rates the pipeline accepts.
var commonRates = map[int64]bool{
	8000: true, 11025: true, 12000: true, 16000: true,
	22050: true, 24000: true, 32000: true, 44100: true,
	48000: true, 64000: true, 88200: true, 96000: true,
}

// IsCommonRate reports whether r is supported.
func IsCommonRate(r int64) bool { return commonRates[r] }

// Resampler converts interleaved mono/stereo PCM between sample rates with a
// windowed-sinc kernel. It is streaming: call Process for each chunk and
// Flush once at the end. The total output frame count is exactly
// round(inputFrames*outRate/inRate) wherever the input is split, so chunk
// boundaries cannot accumulate drift.
type Resampler struct {
	inRate, outRate int64
	channels        int
	taps            int
	half            int

	inFrames  int64
	outFrames int64
	buf       []float64 // interleaved history
	bufStart  int64     // absolute input-frame index of buf[0]
	padFrames int64
}

// NewResampler builds a converter for mono (1) or stereo (2) PCM.
func NewResampler(inRate, outRate int64, channels int) *Resampler {
	if inRate <= 0 || outRate <= 0 {
		panic("audio: non-positive sample rate")
	}
	if channels != 1 && channels != 2 {
		panic("audio: only mono and stereo are supported")
	}
	const taps = 15
	return &Resampler{
		inRate: inRate, outRate: outRate, channels: channels,
		taps: taps, half: (taps - 1) / 2,
	}
}

// Reset clears all buffered state and counters.
func (r *Resampler) Reset() {
	r.inFrames, r.outFrames = 0, 0
	r.buf = r.buf[:0]
	r.bufStart = 0
}

// OutputFramesFor returns the exact output frame count for n input frames.
func (r *Resampler) OutputFramesFor(n int64) int64 {
	return clock.MulDivRound(n, r.outRate, r.inRate)
}

// InputRate and OutputRate expose the configured rates.
func (r *Resampler) InputRate() int64  { return r.inRate }
func (r *Resampler) OutputRate() int64 { return r.outRate }

// Process consumes n interleaved input frames and returns any frames whose
// kernel support is fully available.
func (r *Resampler) Process(in []float64, n int) []float64 {
	if n <= 0 {
		return nil
	}
	need := n * r.channels
	if len(in) < need {
		panic("audio: short input buffer")
	}
	r.buf = append(r.buf, in[:need]...)
	r.inFrames += int64(n)
	return r.pull(r.bufStart+int64(len(r.buf)/r.channels)-1, false)
}

// Flush drains the delay line, returning the remaining output frames.
func (r *Resampler) Flush() []float64 {
	target := r.OutputFramesFor(r.inFrames)
	// Extend with silence so the kernel can reach the final positions.
	pad := r.half + r.taps
	r.buf = append(r.buf, make([]float64, pad*r.channels)...)
	out := r.pull(r.bufStart+int64(len(r.buf)/r.channels)-1, true)
	// Guard against a position just beyond the padded tail.
	for r.outFrames < target {
		out = append(out, r.render(r.outFrames)...)
		r.outFrames++
	}
	r.buf = r.buf[:0]
	return out
}

// pull renders while the kernel's right half stays within lastAbs. When
// force is true it renders until the target count is reached.
func (r *Resampler) pull(lastAbs int64, force bool) []float64 {
	target := r.OutputFramesFor(r.inFrames)
	out := make([]float64, 0, int(target-r.outFrames+1)*r.channels)
	for r.outFrames < target {
		pos := r.posOf(r.outFrames)
		if !force && pos+int64(r.half) > lastAbs {
			break
		}
		out = append(out, r.render(r.outFrames)...)
		r.outFrames++
	}
	// Retain only the frames the next output position still needs.
	if next := r.posOf(r.outFrames); next > int64(r.half) {
		drop := next - int64(r.half) - r.bufStart
		if frames := int64(len(r.buf) / r.channels); drop > 0 && drop < frames {
			r.buf = append(r.buf[:0], r.buf[drop*int64(r.channels):]...)
			r.bufStart += drop
		}
	}
	return out
}

// posOf returns the input-frame position whose phase is used for output frame o.
func (r *Resampler) posOf(o int64) int64 {
	return o * r.inRate / r.outRate
}

func (r *Resampler) fracOf(o int64) float64 {
	return float64((o*r.inRate)%r.outRate) / float64(r.outRate)
}

// sample reads one channel at an absolute frame index, zero outside history.
func (r *Resampler) sample(abs int64, c int) float64 {
	idx := abs - r.bufStart
	if idx < 0 || idx >= int64(len(r.buf)/r.channels) {
		return 0
	}
	return r.buf[int(idx)*r.channels+c]
}

func (r *Resampler) render(o int64) []float64 {
	pos := r.posOf(o)
	frac := r.fracOf(o)
	base := pos - int64(r.half)
	out := make([]float64, r.channels)
	for c := 0; c < r.channels; c++ {
		var acc, norm float64
		for k := 0; k < r.taps; k++ {
			x := float64(base+int64(k)) + frac - float64(pos)
			w := sinc(x) * blackman(x, float64(r.half+1))
			if w == 0 {
				continue
			}
			acc += r.sample(base+int64(k), c) * w
			norm += w
		}
		if norm != 0 {
			out[c] = acc / norm
		}
	}
	return out
}

func sinc(x float64) float64 {
	if math.Abs(x) < 1e-12 {
		return 1
	}
	px := math.Pi * x
	return math.Sin(px) / px
}

func blackman(x, half float64) float64 {
	if math.Abs(x) >= half {
		return 0
	}
	v := math.Pi * x / half
	return 0.42 + 0.5*math.Cos(v) + 0.08*math.Cos(2*v)
}
