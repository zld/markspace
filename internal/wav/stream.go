package wav

import (
	"fmt"
	"io"
)

// ReadFrames decodes up to len(dst)/channels frames into dst as interleaved
// float32 samples and returns the number of frames written. Integer PCM maps
// -32768 (or its 24/32-bit equivalent) to -1.0; IEEE float values pass
// through unchanged. ReadFrames returns io.EOF once the data chunk has been
// consumed.
func (r *Reader) ReadFrames(dst []float32) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	ch := r.header.Channels
	frames := len(dst) / ch
	if max := int(r.remaining / int64(r.header.BlockAlign)); frames > max {
		frames = max
	}
	if frames == 0 {
		return 0, io.EOF
	}
	want := frames * r.header.BlockAlign
	if cap(r.buf) < want {
		r.buf = make([]byte, want)
	}
	buf := r.buf[:want]
	n, err := r.f.ReadAt(buf, r.header.DataOffset+r.consumed)
	if err != nil && err != io.EOF {
		return 0, fmt.Errorf("wav: reading samples: %w", err)
	}
	if n != want {
		return 0, fmt.Errorf("wav: %w: read %d of %d bytes", ErrTruncated, n, want)
	}
	if i, err := decodeSamples(buf, dst[:frames*ch], r.header); err != nil {
		return 0, fmt.Errorf("wav: %w at frame %d", err, i/ch)
	}
	r.consumed += int64(n)
	r.remaining -= int64(n)
	return frames, nil
}
