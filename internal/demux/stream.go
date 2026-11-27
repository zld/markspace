package demux

import "io"

// readerAtSeeker adapts io.ReaderAt to a sequential byte reader while keeping
// the absolute position for error annotation. The size bounds every read so a
// declared run cannot walk past the end of the file.
type readerAtSeeker struct {
	r    io.ReaderAt
	pos  int64
	size int64
}

func (s *readerAtSeeker) ReadByte() (byte, error) {
	var b [1]byte
	_, err := s.r.ReadAt(b[:], s.pos)
	if err != nil {
		return 0, err
	}
	s.pos++
	return b[0], nil
}

func (s *readerAtSeeker) readFull(p []byte) error {
	n, err := s.r.ReadAt(p, s.pos)
	s.pos += int64(n)
	if err == io.EOF && n == len(p) {
		return nil
	}
	return err
}

func (s *readerAtSeeker) readN(n int64) ([]byte, error) {
	buf := make([]byte, n)
	if err := s.readFull(buf); err != nil {
		return nil, err
	}
	return buf, nil
}
