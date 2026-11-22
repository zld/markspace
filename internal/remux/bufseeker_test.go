package remux

// bufSeeker is an in-memory io.WriteSeeker that honors absolute positioning,
// which box writers require to seek back and patch box sizes after EndBox.
// Writes may land mid-buffer (overwrite) or append when at the end.
type bufSeeker struct {
	buf []byte
	pos int64
}

func newBufSeeker() *bufSeeker { return &bufSeeker{} }

func (b *bufSeeker) Write(p []byte) (int, error) {
	end := b.pos + int64(len(p))
	if end > int64(len(b.buf)) {
		b.buf = append(b.buf, make([]byte, end-int64(len(b.buf)))...)
	}
	copy(b.buf[b.pos:end], p)
	b.pos = end
	return len(p), nil
}

func (b *bufSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0: // io.SeekStart
		b.pos = offset
	case 1: // io.SeekCurrent
		b.pos += offset
	case 2: // io.SeekEnd
		b.pos = int64(len(b.buf)) + offset
	}
	if b.pos < 0 {
		return 0, errNegativeSeek
	}
	return b.pos, nil
}

func (b *bufSeeker) Bytes() []byte { return b.buf }

type seekError string

func (e seekError) Error() string { return string(e) }

const errNegativeSeek = seekError("remux: negative seek position")
