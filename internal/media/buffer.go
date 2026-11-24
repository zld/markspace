// Package media writes and inspects the project's deterministic MP4 media
// tracks using github.com/abema/go-mp4.
package media

import "io"

// WriteSeekerBuffer is an in-memory io.WriteSeeker. Seek does not shrink the
// logical length; holes are zero-filled on the next Write.
type WriteSeekerBuffer struct {
	b   []byte
	pos int64
}

func (w *WriteSeekerBuffer) Write(p []byte) (int, error) {
	if w.pos < 0 {
		return 0, io.ErrShortWrite
	}
	end := w.pos + int64(len(p))
	if end > int64(len(w.b)) {
		grown := make([]byte, end)
		copy(grown, w.b)
		w.b = grown
	}
	copy(w.b[w.pos:end], p)
	w.pos = end
	return len(p), nil
}

// Seek implements io.Seeker.
func (w *WriteSeekerBuffer) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = w.pos + offset
	case io.SeekEnd:
		next = int64(len(w.b)) + offset
	default:
		return 0, io.ErrUnexpectedEOF
	}
	if next < 0 {
		return 0, io.ErrUnexpectedEOF
	}
	w.pos = next
	return next, nil
}

// Bytes returns the complete logical buffer.
func (w *WriteSeekerBuffer) Bytes() []byte { return w.b }
