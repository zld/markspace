package probe

import "io"

// boundedReader restricts an underlying ReadSeeker to the absolute byte
// window [base, base+length). go-mp4's generic traversal works in absolute
// offsets, so this lets us point it at exactly one well-formed subtree (the
// moov box) even when a later top-level box such as mdat is truncated.
type boundedReader struct {
	inner  io.ReadSeeker
	base   int64
	length int64
	pos    int64
}

func newBoundedReader(inner io.ReadSeeker, base, length int64) *boundedReader {
	return &boundedReader{inner: inner, base: base, length: length, pos: 0}
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.pos >= b.length {
		return 0, io.EOF
	}
	remaining := b.length - b.pos
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	if _, err := b.inner.Seek(b.base+b.pos, io.SeekStart); err != nil {
		return 0, err
	}
	n, err := b.inner.Read(p)
	b.pos += int64(n)
	return n, err
}

func (b *boundedReader) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = b.pos + offset
	case io.SeekEnd:
		target = b.length + offset
	default:
		return 0, errInvalidWhence
	}
	if target < 0 || target > b.length {
		return 0, errSeekOutOfWindow
	}
	b.pos = target
	return target, nil
}

var (
	errInvalidWhence    = boundedError("invalid seek whence")
	errSeekOutOfWindow  = boundedError("seek outside bounded window")
)

type boundedError string

func (e boundedError) Error() string { return string(e) }
