package fixtures

import "bytes"

// byteBuffer is an in-memory io.WriteSeeker so boxes can be laid out without
// touching the filesystem twice.
type byteBuffer struct {
	buf *bytes.Buffer
	off int64
}

func (b *byteBuffer) Write(p []byte) (int, error) {
	if b.buf == nil {
		b.buf = &bytes.Buffer{}
	}
	if b.off < int64(b.buf.Len()) {
		// Overwrite surviving tail (used by EndBox size patching).
		tmp := b.buf.Bytes()
		n := copy(tmp[b.off:], p)
		if n < len(p) {
			b.buf.Write(p[n:])
		}
	} else {
		for int64(b.buf.Len()) < b.off {
			b.buf.WriteByte(0)
		}
		b.buf.Write(p)
	}
	b.off += int64(len(p))
	return len(p), nil
}

func (b *byteBuffer) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		b.off = offset
	case 1:
		b.off += offset
	case 2:
		b.off = int64(b.buf.Len()) + offset
	}
	if b.off < 0 {
		b.off = 0
	}
	return b.off, nil
}

func (b *byteBuffer) Len() int {
	if b.buf == nil {
		return 0
	}
	return b.buf.Len()
}

func (b *byteBuffer) Bytes() []byte {
	if b.buf == nil {
		return nil
	}
	return b.buf.Bytes()
}

// byteCounter only measures writes; it discards payloads (its seek position
// tracking is enough for the box-size computation pass).
type byteCounter []byte

func (c *byteCounter) Write(p []byte) (int, error) {
	*c = append(*c, p...)
	return len(p), nil
}

func (c *byteCounter) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		if int64(len(*c)) < offset {
			grow := offset - int64(len(*c))
			*c = append(*c, make([]byte, grow)...)
		}
		return offset, nil
	case 1:
		return int64(len(*c)) + offset, nil
	default:
		return int64(len(*c)) + offset, nil
	}
}
