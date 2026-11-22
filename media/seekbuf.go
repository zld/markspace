package media

import (
	"errors"
	"io"
)

// errSeekOutOfRange is returned for seeks outside the allocated region.
var errSeekOutOfRange = errors.New("seek out of range")

// bytesReadSeeker is an in-memory io.WriteSeeker/ReadSeeker backed by a
// growable byte slice. go-mp4's Writer requires seeking while it patches
// box sizes, which bytes.Buffer cannot provide.
type bytesReadSeeker struct {
	data []byte
	pos  int64
}

func (b *bytesReadSeeker) Write(p []byte) (int, error) {
	end := int(b.pos) + len(p)
	if end > len(b.data) {
		grown := make([]byte, end)
		copy(grown, b.data)
		b.data = grown
	}
	copy(b.data[b.pos:], p)
	b.pos += int64(len(p))
	return len(p), nil
}

func (b *bytesReadSeeker) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = b.pos + offset
	case io.SeekEnd:
		target = int64(len(b.data)) + offset
	}
	if target < 0 || target > int64(len(b.data)) {
		return 0, errSeekOutOfRange
	}
	b.pos = target
	return target, nil
}

func (b *bytesReadSeeker) Read(p []byte) (int, error) {
	if b.pos >= int64(len(b.data)) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.pos:])
	b.pos += int64(n)
	return n, nil
}

// Bytes returns the accumulated content.
func (b *bytesReadSeeker) Bytes() []byte { return b.data }
