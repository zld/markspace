// Package media: in-memory seekable writer used by the box writer.
package media

import (
	"io"

	"example.com/assetpack/internal/fault"
)

// memWriteSeeker is an in-memory io.WriteSeeker for the box writer.
type memWriteSeeker struct {
	buf []byte
	pos int64
}

func (m *memWriteSeeker) Write(p []byte) (int, error) {
	end := int(m.pos) + len(p)
	if end > len(m.buf) {
		grown := make([]byte, end)
		copy(grown, m.buf)
		m.buf = grown
	}
	copy(m.buf[m.pos:], p)
	m.pos = int64(end)
	return len(p), nil
}

func (m *memWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = m.pos + offset
	case io.SeekEnd:
		next = int64(len(m.buf)) + offset
	default:
		return 0, fault.New(fault.StageAudio, fault.KindInvalidArg, "bad seek whence %d", whence)
	}
	if next < 0 {
		return 0, fault.New(fault.StageAudio, fault.KindInvalidArg, "negative seek")
	}
	if next > int64(len(m.buf)) {
		grown := make([]byte, next)
		copy(grown, m.buf)
		m.buf = grown
	}
	m.pos = next
	return next, nil
}
