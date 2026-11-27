// Package audio implements the deterministic PCM processing chain:
// integer-phase resampling with cross-packet filter state, channel layout
// normalization and loudness/true-peak control.
package audio

// Buffer is interleaved PCM in float64 samples: len(Samples) == Frames*Ch.
type Buffer struct {
	Samples []float64
	Frames  int
	Ch      int
}

// NewBuffer allocates a zeroed buffer.
func NewBuffer(frames, ch int) Buffer {
	return Buffer{Samples: make([]float64, frames*ch), Frames: frames, Ch: ch}
}

// Frame copies one interleaved frame out.
func (b Buffer) Frame(i int) []float64 {
	return b.Samples[i*b.Ch : (i+1)*b.Ch]
}

// Append concatenates another buffer with matching channel count.
func (b *Buffer) Append(o Buffer) {
	if b.Ch == 0 {
		b.Ch = o.Ch
	}
	if o.Ch != b.Ch {
		panic("audio: append channel mismatch")
	}
	b.Samples = append(b.Samples, o.Samples...)
	b.Frames += o.Frames
}
