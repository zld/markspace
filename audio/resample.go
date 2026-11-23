package audio

import "mediacore/media"

// Resampler converts interleaved PCM between sample rates with linear
// interpolation. It is streaming: state carries across Process calls so
// sample time stays continuous, and Flush emits the sub-frame tail by
// extending the final sample rather than dropping it.
type Resampler struct {
	inRate, outRate, ch int
	prev, cur           []float32
	base                int64 // input frame index of prev
	fed                 int64 // total input frames consumed
	out                 int64 // total output frames emitted
	flushed             bool
}

// NewResampler builds a resampler; rates must be positive.
func NewResampler(inRate, outRate, channels int) (*Resampler, error) {
	if inRate <= 0 || outRate <= 0 || channels <= 0 {
		return nil, media.Fault(media.CodeInternal, "audio.NewResampler", -1,
			"invalid rates %d->%d or channels %d", inRate, outRate, channels)
	}
	return &Resampler{
		inRate: inRate, outRate: outRate, ch: channels,
		prev: make([]float32, channels),
		cur:  make([]float32, channels),
	}, nil
}

// InFrames returns total input frames consumed.
func (r *Resampler) InFrames() int64 { return r.fed }

// OutFrames returns total output frames emitted.
func (r *Resampler) OutFrames() int64 { return r.out }

// Process consumes one chunk of interleaved input and returns the
// interleaved output that becomes available. Input length must be a
// whole number of frames.
func (r *Resampler) Process(in []float32) ([]float32, error) {
	if len(in)%r.ch != 0 {
		return nil, media.Fault(media.CodeInternal, "audio.Resampler.Process", int64(len(in)),
			"sample count %d not a multiple of %d channels", len(in), r.ch)
	}
	var out []float32
	for i := 0; i < len(in); i += r.ch {
		frame := in[i : i+r.ch]
		if r.fed == 0 {
			copy(r.prev, frame)
			r.base = 0
			r.fed++
			continue
		}
		copy(r.cur, frame)
		out = r.emit(out)
		copy(r.prev, r.cur)
		r.base++
		r.fed++
	}
	return out, nil
}

// emit produces every output whose position falls in [base, base+1).
// Positions are compared in exact rational arithmetic (n*inRate vs
// (base+1)*outRate) so the output count never drifts with float error.
func (r *Resampler) emit(out []float32) []float32 {
	for r.out*int64(r.inRate) < (r.base+1)*int64(r.outRate) {
		pos := float64(r.out*int64(r.inRate)) / float64(r.outRate)
		frac := float32(pos - float64(r.base))
		if frac > 1 {
			frac = 1
		}
		for c := 0; c < r.ch; c++ {
			out = append(out, r.prev[c]+frac*(r.cur[c]-r.prev[c]))
		}
		r.out++
	}
	return out
}

// Flush drains the sub-frame tail: the final interval is completed by
// holding the last sample, so total output equals
// round(inFrames * outRate / inRate) and sample time stays continuous.
func (r *Resampler) Flush() []float32 {
	if r.flushed {
		return nil
	}
	r.flushed = true
	want := (r.fed*int64(r.outRate) + int64(r.inRate)/2) / int64(r.inRate)
	var out []float32
	copy(r.cur, r.prev) // hold last sample across the tail interval
	for r.out < want {
		out = r.emit(out)
		r.base++ // advance the held interval until the quota is met
	}
	return out
}
