package fixtures

// seekBuffer is an in-memory file-like sink satisfying io.WriteSeeker, which
// go-mp4's Writer needs for back-patching box sizes. Writes past the current
// end extend the slice; writes inside it overwrite in place.
type seekBuffer struct {
	data []byte
	pos  int
}

func newSeekBuffer() *seekBuffer { return &seekBuffer{} }

func (s *seekBuffer) Write(p []byte) (int, error) {
	end := s.pos + len(p)
	if end > len(s.data) {
		s.data = append(s.data, make([]byte, end-len(s.data))...)
	}
	copy(s.data[s.pos:end], p)
	s.pos = end
	return len(p), nil
}

func (s *seekBuffer) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		s.pos = int(offset)
	case 1:
		s.pos += int(offset)
	case 2:
		s.pos = len(s.data) + int(offset)
	}
	if s.pos < 0 {
		s.pos = 0
	}
	return int64(s.pos), nil
}

func (s *seekBuffer) Bytes() []byte { return s.data }
