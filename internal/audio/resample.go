// Package audio implements the PCM path: streaming resampling with continuous
// filter state, channel-layout normalisation and loudness control with peak
// limiting.  Samples stay in the 16-bit domain used by the container, and no
// stage allocates a whole track at once.
package audio

import (
	"errors"
	"fmt"
	"math"
)

// Errors shared by the audio package.
var (
	ErrRateUnsupported = errors.New("audio: sample rate is not 44100 or 48000")
	ErrBadBlock        = errors.New("audio: block is not a whole frame")
)

// tapCount and cutoffTune are fixed so resampling is reproducible.
const (
	// tapCount sets the transition width.  192 taps with a Blackman window
	// gives roughly 1 kHz transition at 44.1 kHz, enough to reject content just
	// above the 20 kHz band the platform forwards.
	tapCount   = 192
	cutoffTune = 0.85
	// flushFrames bounds the trailing zero pad so Flush is cheap.
	flushFrames = 96
)

// Resampler converts interleaved 44.1 kHz or 48 kHz PCM to 48 kHz.
//
// It is a polyphase windowed-sinc interpolator.  The absolute input position
// is carried across calls, so feeding the stream in blocks yields exactly the
// same output as one long call.  Edges are explicit: half-kernel zeros precede
// frame zero, Flush injects the same number of trailing zeros, and both are
// reported through EdgePolicy and counted in quality statistics.
type Resampler struct {
	InRate   int
	OutRate  int
	Channels int

	cut        float64
	windowHalf float64
	half       int
	branches   [][]float64

	hist       []float64 // retained frames, interleaved
	histFrames int
	absNext    int64 // absolute index of the next input frame
	outIndex   int64
	stepNum    int64
	stepDen    int64
	headPad    int
	tailPad    int
	flushed    bool
}

// kernelValue evaluates the band-limited interpolation kernel at distance d
// (input frames) from its centre.  The Blackman taper is centred on d=0 and
// reaches zero at +/- windowHalf, so every fractional phase shares one window
// extent and its DC sum is therefore constant.
func kernelValue(d, cut, windowHalf float64) float64 {
	if d < -windowHalf || d > windowHalf {
		return 0
	}
	s := 2 * cut * sinc(2*cut*d)
	t := (d + windowHalf) / (2 * windowHalf) // 0..1 across the window
	return s * blackman(t)
}

// blackman evaluates a periodic Blackman window at t in [0,1].
func blackman(t float64) float64 {
	return 0.42 - 0.5*math.Cos(2*math.Pi*t) + 0.08*math.Cos(4*math.Pi*t)
}

func normalise(k []float64) {
	var sum float64
	for _, v := range k {
		sum += v
	}
	if sum == 0 {
		return
	}
	for i := range k {
		k[i] /= sum
	}
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

func gcdInt(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// NewResampler builds a converter from inRate to outRate for channels.
// Only 44.1 kHz or 48 kHz input and 48 kHz output are accepted.
func NewResampler(inRate, outRate, channels int) (*Resampler, error) {
	if inRate != 44100 && inRate != 48000 {
		return nil, fmt.Errorf("%w: input %d", ErrRateUnsupported, inRate)
	}
	if outRate != 48000 {
		return nil, fmt.Errorf("%w: output %d", ErrRateUnsupported, outRate)
	}
	if channels <= 0 || channels > 8 {
		return nil, fmt.Errorf("audio: unsupported channel count %d", channels)
	}
	r := &Resampler{InRate: inRate, OutRate: outRate, Channels: channels, half: tapCount / 2}
	r.cut = 0.5 * float64(inRate) / float64(outRate)
	if r.cut > 0.5 {
		r.cut = 0.5
	}
	// Only downsampling needs the cutoff pulled in; 48->48 keeps full band.
	if inRate > outRate {
		r.cut *= cutoffTune
	}
	r.windowHalf = float64(r.half) - 0.5
	g := gcdInt(int64(inRate), int64(outRate))
	r.stepNum = int64(inRate) / g
	r.stepDen = int64(outRate) / g
	r.branches = make([][]float64, r.stepNum)
	for p := int64(0); p < r.stepNum; p++ {
		frac := float64(p) / float64(r.stepNum)
		b := make([]float64, tapCount)
		for i := range b {
			b[i] = kernelValue(float64(i-r.half)-frac, r.cut, r.windowHalf)
		}
		normalise(b)
		r.branches[p] = b
	}
	r.Reset()
	return r, nil
}

// Reset clears state and re-primes the head padding.
func (r *Resampler) Reset() {
	r.headPad = r.half
	r.tailPad = 0
	r.flushed = false
	r.absNext = 0
	r.outIndex = 0
	// Head padding: half-kernel zeros represent absolute frames
	// [-headPad, -1]; their positions are tracked through absNext.
	r.histFrames = r.half
	r.hist = make([]float64, r.half*r.Channels)
}

// Convert appends one interleaved block and returns newly computable frames.
func (r *Resampler) Convert(block []float64) ([]float64, error) {
	if len(block)%r.Channels != 0 {
		return nil, ErrBadBlock
	}
	if r.flushed {
		return nil, errors.New("audio: Convert called after Flush")
	}
	frames := len(block) / r.Channels
	r.hist = append(r.hist, block...)
	r.histFrames += frames
	r.absNext += int64(frames)
	return r.drain()
}

// drain emits every output frame whose window lies inside available history.
func (r *Resampler) drain() ([]float64, error) {
	var out []float64
	for {
		// Output frame k sits at input position k * InRate / OutRate.
		num := r.outIndex * r.stepNum
		den := r.stepDen
		pos := num / den
		frac := num % den
		// Kernel index i sits at input distance (i-half)-frac, so the window
		// covers frames [pos-half, pos+half-1].
		windowEnd := pos + int64(r.half) - 1
		if windowEnd >= r.absNext {
			break
		}
		phase := int(frac * r.stepNum / den)
		if phase < 0 || phase >= len(r.branches) {
			phase = 0
		}
		kern := r.branches[phase]
		for c := 0; c < r.Channels; c++ {
			var acc float64
			for i := 0; i < len(kern); i++ {
				idx := pos - int64(r.half) + int64(i)
				off, ok := r.offsetFor(idx)
				if !ok {
					continue
				}
				acc += kern[i] * r.hist[off+c]
			}
			out = append(out, acc)
		}
		r.outIndex++
	}
	r.compact()
	return out, nil
}

func (r *Resampler) offsetFor(idx int64) (int, bool) {
	start := r.absNext - int64(r.histFrames)
	rel := idx - start
	if rel < 0 || rel >= int64(r.histFrames) {
		return 0, false
	}
	return int(rel) * r.Channels, true
}

func (r *Resampler) compact() {
	// Keep one full kernel of frames as the widest future window.
	keep := tapCount
	if r.histFrames <= keep {
		return
	}
	drop := r.histFrames - keep
	r.hist = r.hist[drop*r.Channels:]
	r.histFrames = keep
}

// Flush pads the tail with half-kernel zeros and returns the final frames.
func (r *Resampler) Flush() ([]float64, error) {
	if r.flushed {
		return nil, nil
	}
	r.flushed = true
	r.tailPad = r.half
	origNext := r.absNext
	r.hist = append(r.hist, make([]float64, r.half*r.Channels)...)
	r.histFrames += r.half
	r.absNext += int64(r.half)
	out, err := r.drain()
	r.absNext = origNext
	return out, err
}

// EdgePolicy reports head and tail padding frames injected.
func (r *Resampler) EdgePolicy() (headPad, tailPad int) {
	return r.headPad, r.tailPad
}

// OutputFrames returns the number of output frames emitted so far.
func (r *Resampler) OutputFrames() int64 { return r.outIndex }
