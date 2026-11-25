package fixture

import (
	"errors"
	"io"
)

// memSeeker is an in-memory io.WriteSeeker, which go-mp4's writer requires
// so it can backpatch box sizes.
type memSeeker struct {
	buf []byte
	off int64
}

func (m *memSeeker) Write(p []byte) (int, error) {
	end := m.off + int64(len(p))
	if end > int64(len(m.buf)) {
		m.buf = append(m.buf, make([]byte, end-int64(len(m.buf)))...)
	}
	copy(m.buf[m.off:end], p)
	m.off = end
	return len(p), nil
}

func (m *memSeeker) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = m.off + offset
	case io.SeekEnd:
		next = int64(len(m.buf)) + offset
	default:
		return 0, errors.New("fixture: invalid seek whence")
	}
	if next < 0 {
		return 0, errors.New("fixture: negative seek")
	}
	m.off = next
	return next, nil
}
