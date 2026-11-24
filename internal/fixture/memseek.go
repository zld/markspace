package fixture

import (
	"errors"
	"io"
)

// memWriteSeeker is the minimal io.WriteSeeker needed by mp4.Writer while a
// box tree is assembled in memory. Sparse writes pad with zeroes so callers
// may patch a box after it has been laid out.
type memWriteSeeker struct {
	buf []byte
	pos int64
}

var errNegativeSeek = errors.New("fixture: negative seek")

func (m *memWriteSeeker) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if end < 0 {
		return 0, errNegativeSeek
	}
	for int64(len(m.buf)) < end {
		m.buf = append(m.buf, 0)
	}
	copy(m.buf[m.pos:], p)
	m.pos = end
	return len(p), nil
}

func (m *memWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = m.pos
	case io.SeekEnd:
		base = int64(len(m.buf))
	default:
		return 0, errNegativeSeek
	}
	next := base + offset
	if next < 0 {
		return 0, errNegativeSeek
	}
	m.pos = next
	return next, nil
}

// Patch writes b at the given absolute offset without moving the cursor.
func (m *memWriteSeeker) Patch(offset int, b []byte) {
	copy(m.buf[offset:], b)
}
