package mp4

import "io"

// seekBuffer is an in-memory io.WriteSeeker. go-mp4's writer seeks backward to
// patch box sizes, which bytes.Buffer cannot do.
type seekBuffer struct {
	b   []byte
	pos int64
}

func (s *seekBuffer) Write(p []byte) (int, error) {
	need := s.pos + int64(len(p))
	if need > int64(len(s.b)) {
		s.b = append(s.b, make([]byte, need-int64(len(s.b)))...)
	}
	copy(s.b[s.pos:], p)
	s.pos = need
	return len(p), nil
}

func (s *seekBuffer) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = s.pos + offset
	case io.SeekEnd:
		target = int64(len(s.b)) + offset
	}
	if target < 0 {
		return 0, io.EOF
	}
	s.pos = target
	return target, nil
}

// Bytes returns the populated portion of the buffer.
func (s *seekBuffer) Bytes() []byte { return s.b }

// Len is the current logical size.
func (s *seekBuffer) Len() int { return len(s.b) }
