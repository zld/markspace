package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// ftypPayload builds a minimal ftyp body: major brand "mdmd", minor version 0,
// no compatible brands. The go-mp4 writer supplies the 8-byte box header.
func ftypPayload() []byte {
	return []byte{
		'm', 'd', 'm', 'd', // major_brand
		0, 0, 0, 0, // minor_version
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// bufferSeeker adapts a bytes.Buffer to io.WriteSeeker for the mp4 writer,
// which needs Seek to patch box sizes after EndBox.
type bufferSeeker struct {
	buf *bytes.Buffer
	pos int64
}

func newBufferSeeker(buf *bytes.Buffer) *bufferSeeker {
	return &bufferSeeker{buf: buf}
}

func (s *bufferSeeker) Write(p []byte) (int, error) {
	// Grow / overwrite semantics matching an in-memory file.
	end := s.pos + int64(len(p))
	if end > int64(s.buf.Len()) {
		if s.pos > int64(s.buf.Len()) {
			return 0, errors.New("manifest: seek beyond buffer end")
		}
		// Extend by writing the tail; first cover any in-place overwrite.
		head := s.buf.Bytes()[:s.pos]
		s.buf.Reset()
		s.buf.Write(head)
		s.buf.Write(p)
	} else {
		copy(s.buf.Bytes()[s.pos:], p)
	}
	s.pos = end
	return len(p), nil
}

func (s *bufferSeeker) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case 0:
		target = offset
	case 1:
		target = s.pos + offset
	case 2:
		target = int64(s.buf.Len()) + offset
	default:
		return 0, errors.New("manifest: unknown seek whence")
	}
	if target < 0 || target > int64(s.buf.Len()) {
		return 0, errors.New("manifest: seek out of range")
	}
	s.pos = target
	return target, nil
}
