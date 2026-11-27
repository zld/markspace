package audio

import (
	"github.com/example/mediad/internal/model"
)

// Resampler converts PCM between sample rates using linear interpolation on
// an integer fixed-point grid. Output sample i sits at input position
// i*from/to; the integer part selects the left input frame and the
// fractional part is the interpolation weight. State (input/output counters
// and the previous packet's last two frames) is retained across packets, and
// output positions past the last real input frame are interpolated toward a
// fixed zero endpoint frame.
type Resampler struct {
	from, to uint32
	ch       int
	flushed  bool
	prev     []float64 // last input frame seen
	prev2    []float64 // second-to-last input frame
	hasPrev  bool
	inTotal  uint64 // input frames consumed across packets
	outTotal uint64 // output frames emitted across packets
}

const gridShift = 32
const gridOne = 1 << gridShift

// NewResampler builds a stateful resampler. ch is the channel count of every
// buffer passed in.
func NewResampler(from, to uint32, ch int) *Resampler {
	if from == 0 || to == 0 || ch <= 0 {
		panic("audio: invalid resampler parameters")
	}
	return &Resampler{from: from, to: to, ch: ch}
}

// Ratio returns the configured rates for bookkeeping.
func (r *Resampler) Ratio() (from, to uint32) { return r.from, r.to }

// step is the input-position advance per output sample in fixed point.
func (r *Resampler) step() uint64 {
	return (uint64(r.from) << gridShift) / uint64(r.to)
}

// pos maps output index i to its absolute fixed-point input position.
func (r *Resampler) pos(i uint64) uint64 {
	return i * r.step()
}

// ExpectedOutput is the floor output count whose interpolation stays within
// the supplied input frames (no zero endpoint needed).
func (r *Resampler) ExpectedOutput(inFrames uint64) uint64 {
	return inFrames * uint64(r.to) / uint64(r.from)
}

// RoundedOutput is the nearest-whole output target; Flush reaches it,
// zero-padding any position beyond the last real frame.
func (r *Resampler) RoundedOutput(inFrames uint64) uint64 {
	return (inFrames*uint64(r.to) + uint64(r.from)/2) / uint64(r.from)
}

// sampleAt interpolates one channel frame at absolute fixed-point position p
// over the full known input (this packet's frames plus carried prev/prev2).
func (r *Resampler) sampleAt(p uint64, in Buffer) []float64 {
	idx := p >> gridShift       // left input frame index (absolute)
	frac := p & (gridOne - 1)  // weight within [idx, idx+1)
	first := r.inTotal         // absolute index of in.Frame(0)
	w := float64(frac) / float64(gridOne)
	frame := make([]float64, r.ch)

	left := r.inputFrame(first, idx, in)
	right := r.inputFrame(first, idx+1, in)
	// A right endpoint just past the known media but within one input span of
	// a real frame extrapolates from the last known frame; the strict zero
	// endpoint only applies one full span beyond it (handled in Flush).
	if right == nil && left != nil {
		right = left
	}
	for c := 0; c < r.ch; c++ {
		lv := 0.0
		if left != nil {
			lv = left[c]
		}
		rv := 0.0
		if right != nil {
			rv = right[c]
		}
		frame[c] = lv + (rv-lv)*w
	}
	return frame
}

// inputFrame resolves an absolute input frame index to a sample slice. An
// index beyond every real frame yields nil (the virtual zero region); an
// index just before the current packet resolves to carried boundary frames.
func (r *Resampler) inputFrame(first, idx uint64, in Buffer) []float64 {
	switch {
	case idx >= first+uint64(in.Frames):
		return nil // virtual zero frame beyond known media
	case idx >= first:
		return in.Frame(int(idx - first))
	case r.hasPrev && idx == first-1:
		return r.prev
	case r.hasPrev && idx == first-2 && r.prev2 != nil:
		return r.prev2
	default:
		// Before any known frame: treat as zero (only reachable on flush).
		return nil
	}
}

// Process resamples one packet's worth of input frames.
func (r *Resampler) Process(in Buffer) (Buffer, error) {
	if in.Ch != r.ch {
		return Buffer{}, model.NewError(model.CodeIllegalPacket, model.StageResample,
			0, -1, "resample: channel count changed mid-stream")
	}
	out := NewBuffer(0, r.ch)
	first := r.inTotal
	// Output indices whose left input frame lies inside this packet and
	// whose position does not cross the packet's right boundary.
	i := r.outTotal
	for {
		p := r.pos(i)
		idx := p >> gridShift
		if idx >= first+uint64(in.Frames) {
			break // belongs to a future packet or the flush/zero region
		}
		out.Samples = append(out.Samples, r.sampleAt(p, in)...)
		out.Frames++
		i++
	}
	// Carry boundary frames into the next packet.
	if in.Frames > 0 {
		if r.hasPrev {
			r.prev2 = append(r.prev2[:0], r.prev...)
		}
		r.prev = append(r.prev[:0], in.Frame(in.Frames-1)...)
		r.hasPrev = true
	}
	r.inTotal += uint64(in.Frames)
	r.outTotal = i
	return out, nil
}

// Flush extends output to the rounded target, interpolating any position at
// or beyond the last real frame toward a zero endpoint one input span after
// it. Repeated calls after the first return an empty buffer.
func (r *Resampler) Flush() Buffer {
	if r.flushed || !r.hasPrev {
		r.flushed = true
		return NewBuffer(0, r.ch)
	}
	want := r.RoundedOutput(r.inTotal)
	out := NewBuffer(0, r.ch)
	empty := Buffer{Ch: r.ch} // no current packet; all frames resolve via prev/zero
	for r.outTotal+uint64(out.Frames) < want {
		p := r.pos(r.outTotal + uint64(out.Frames))
		out.Samples = append(out.Samples, r.sampleAt(p, empty)...)
		out.Frames++
	}
	r.outTotal += uint64(out.Frames)
	r.flushed = true
	return out
}
