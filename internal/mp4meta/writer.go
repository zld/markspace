package mp4meta

import (
	"bytes"
	"fmt"
	"io"
)

// seekWriter adapts a bytes.Buffer to the io.WriteSeeker the mp4 writer needs.
type seekWriter struct {
	buf *bytes.Buffer
	pos int64
}

func (s *seekWriter) Write(p []byte) (int, error) {
	end := s.pos + int64(len(p))
	if end > int64(s.buf.Len()) {
		extra := end - int64(s.buf.Len())
		s.buf.Write(make([]byte, extra))
	}
	copy(s.buf.Bytes()[s.pos:], p)
	s.pos = end
	return len(p), nil
}

func (s *seekWriter) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = off
	case io.SeekCurrent:
		s.pos += off
	case io.SeekEnd:
		s.pos = int64(s.buf.Len()) + off
	default:
		return 0, fmt.Errorf("mp4meta: invalid whence %d", whence)
	}
	if s.pos < 0 {
		return 0, fmt.Errorf("mp4meta: negative seek %d", s.pos)
	}
	return s.pos, nil
}

func lastTime(tr Track) int64 {
	if len(tr.Times) == 0 {
		return 0
	}
	return tr.Times[len(tr.Times)-1]
}

func max32(v uint32) int64 {
	if v == 0 {
		return 1
	}
	return int64(v)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
